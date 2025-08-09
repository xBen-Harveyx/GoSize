package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"
)

type item struct {
	Path string
	Size int64
}

type minHeap struct {
	mu   sync.Mutex
	data []item
	k    int
}

// push keeps only the largest k elements overall by using a min-heap behavior.
func (h *minHeap) push(it item) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.k <= 0 {
		return
	}
	if len(h.data) < h.k {
		h.data = append(h.data, it)
		h.up(len(h.data) - 1)
		return
	}
	// If new item is larger than smallest, replace root.
	if it.Size > h.data[0].Size {
		h.data[0] = it
		h.down(0)
	}
}

func (h *minHeap) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if h.data[p].Size <= h.data[i].Size {
			break
		}
		h.data[p], h.data[i] = h.data[i], h.data[p]
		i = p
	}
}

func (h *minHeap) down(i int) {
	n := len(h.data)
	for {
		l := 2*i + 1
		r := l + 1
		small := i
		if l < n && h.data[l].Size < h.data[small].Size {
			small = l
		}
		if r < n && h.data[r].Size < h.data[small].Size {
			small = r
		}
		if small == i {
			break
		}
		h.data[i], h.data[small] = h.data[small], h.data[i]
		i = small
	}
}

func (h *minHeap) sortedDesc() []item {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]item, len(h.data))
	copy(out, h.data)
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// humanize bytes without external deps.
func humanBytes(n int64) string {
	const (
		_          = iota
		KB  int64  = 1 << (10 * iota)
		MB
		GB
		TB
		PB
	)
	f := func(v float64, unit string) string { return fmt.Sprintf("%.2f %s", v, unit) }
	switch {
	case n >= PB:
		return f(float64(n)/float64(PB), "PB")
	case n >= TB:
		return f(float64(n)/float64(TB), "TB")
	case n >= GB:
		return f(float64(n)/float64(GB), "GB")
	case n >= MB:
		return f(float64(n)/float64(MB), "MB")
	case n >= KB:
		return f(float64(n)/float64(KB), "MB") // Deliberate? No—use KB here
	}
	return fmt.Sprintf("%d B", n)
}

func humanBytesFixed(n int64) string {
	const (
		KB = 1 << 10
		MB = 1 << 20
		GB = 1 << 30
		TB = 1 << 40
		PB = 1 << 50
	)
	switch {
	case n >= PB:
		return fmt.Sprintf("%.2f PB", float64(n)/float64(PB))
	case n >= TB:
		return fmt.Sprintf("%.2f TB", float64(n)/float64(TB))
	case n >= GB:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(GB))
	case n >= MB:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(MB))
	case n >= KB:
		return fmt.Sprintf("%.2f KB", float64(n)/float64(KB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

type walkCfg struct {
	topK          int
	workers       int
	followLinks   bool
	maxDepth      int // 0 means unlimited
	skipHidden    bool
	skipPatterns  []string
	showProgress  bool
}

type stats struct {
	filesSeen int64
	dirsSeen  int64
	skipped   int64
	errors    int64
}

func main() {
	var (
		topK        = flag.Int("top", 20, "number of largest files and directories to keep")
		workers     = flag.Int("workers", runtime.NumCPU(), "concurrent directory workers")
		rootsFlag   = flag.String("roots", "", "comma-separated roots to scan (default: detect all drives, e.g. C:\\, D:\\)")
		followLinks = flag.Bool("followlinks", false, "follow symlinks/junctions (off by default to avoid cycles)")
		maxDepth    = flag.Int("maxdepth", 0, "max directory depth to scan (0 = unlimited)")
		skipHidden  = flag.Bool("skiphidden", false, "skip hidden files and directories")
		skipGlobs   = flag.String("skip", "", "comma-separated filepath.Match patterns to skip (e.g. \"C:\\\\Windows\\\\*,C:\\\\Program Files\\\\*\")")
		progress    = flag.Bool("progress", true, "periodically print progress to stderr")
	)
	flag.Parse()

	cfg := walkCfg{
		topK:         *topK,
		workers:      *workers,
		followLinks:  *followLinks,
		maxDepth:     *maxDepth,
		skipHidden:   *skipHidden,
		showProgress: *progress,
	}
	if *skipGlobs != "" {
		parts := strings.Split(*skipGlobs, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				cfg.skipPatterns = append(cfg.skipPatterns, p)
			}
		}
	}

	roots := []string{}
	if *rootsFlag != "" {
		for _, r := range strings.Split(*rootsFlag, ",") {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			if !strings.HasSuffix(r, `\`) && !strings.HasSuffix(r, "/") {
				r += `\`
			}
			roots = append(roots, r)
		}
	} else {
		roots = detectWindowsDrives()
		if len(roots) == 0 {
			fmt.Fprintln(os.Stderr, "No drives detected. Provide -roots like -roots=C:\\,D:\\")
			os.Exit(2)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fileTop := &minHeap{k: cfg.topK}
	dirTop := &minHeap{k: cfg.topK}
	var s stats

	// Worker pool controlled by a semaphore channel.
	sem := make(chan struct{}, cfg.workers)

	start := time.Now()
	var wg sync.WaitGroup
	for _, root := range roots {
		r := root
		wg.Add(1)
		go func() {
			defer wg.Done()
			if size, err := walkDir(ctx, r, 0, cfg, sem, fileTop, dirTop, &s); err != nil {
				// permission errors and transient errors are expected sometimes
				_ = size
			}
		}()
	}

	// Optional progress ticker
	done := make(chan struct{})
	if cfg.showProgress {
		go func() {
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					ff := atomic.LoadInt64(&s.filesSeen)
					dd := atomic.LoadInt64(&s.dirsSeen)
					sk := atomic.LoadInt64(&s.skipped)
					er := atomic.LoadInt64(&s.errors)
					fmt.Fprintf(os.Stderr, "[%s] scanned files=%d dirs=%d skipped=%d errors=%d\n",
						time.Since(start).Truncate(time.Millisecond), ff, dd, sk, er)
				}
			}
		}()
	}

	wg.Wait()
	close(done)

	// Print results
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Println()
	fmt.Println("Largest Directories")
	fmt.Fprintln(w, "RANK\tSIZE\tPATH")
	for i, it := range dirTop.sortedDesc() {
		fmt.Fprintf(w, "%d\t%s\t%s\n", i+1, humanBytesFixed(it.Size), it.Path)
	}
	w.Flush()

	fmt.Println()
	fmt.Println("Largest Files")
	w = tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RANK\tSIZE\tPATH")
	for i, it := range fileTop.sortedDesc() {
		fmt.Fprintf(w, "%d\t%s\t%s\n", i+1, humanBytesFixed(it.Size), it.Path)
	}
	w.Flush()

	ff := atomic.LoadInt64(&s.filesSeen)
	dd := atomic.LoadInt64(&s.dirsSeen)
	sk := atomic.LoadInt64(&s.skipped)
	er := atomic.LoadInt64(&s.errors)
	fmt.Println()
	fmt.Printf("Scanned %d files in %d directories in %s (skipped=%d, errors=%d)\n",
		ff, dd, time.Since(start).Truncate(time.Millisecond), sk, er)
}

func walkDir(ctx context.Context, path string, depth int, cfg walkCfg, sem chan struct{}, fileTop, dirTop *minHeap, s *stats) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}

	// Honor depth limit
	if cfg.maxDepth > 0 && depth > cfg.maxDepth {
		atomic.AddInt64(&s.skipped, 1)
		return 0, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		atomic.AddInt64(&s.errors, 1)
		return 0, err
	}
	atomic.AddInt64(&s.dirsSeen, 1)

	var total int64
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, de := range entries {
		name := de.Name()
		full := filepath.Join(path, name)

		if shouldSkipByGlob(full, cfg.skipPatterns) {
			atomic.AddInt64(&s.skipped, 1)
			continue
		}

		info, lerr := de.Info()
		if lerr != nil {
			atomic.AddInt64(&s.errors, 1)
			continue
		}

		if info.Mode()&fs.ModeSymlink != 0 && !cfg.followLinks {
			atomic.AddInt64(&s.skipped, 1)
			continue
		}

		if cfg.skipHidden && strings.HasPrefix(name, ".") {
			atomic.AddInt64(&s.skipped, 1)
			continue
		}

		if de.IsDir() {
			// Try to run this subtree in parallel
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func(p string) {
					defer wg.Done()
					defer func() { <-sem }()
					size, derr := walkDir(ctx, p, depth+1, cfg, sem, fileTop, dirTop, s)
					if derr == nil {
						mu.Lock()
						total += size
						mu.Unlock()
						dirTop.push(item{Path: p, Size: size})
					} else if !isIgnorable(derr) {
						atomic.AddInt64(&s.errors, 1)
					}
				}(full)
			default:
				// No free slot — process synchronously
				size, derr := walkDir(ctx, full, depth+1, cfg, sem, fileTop, dirTop, s)
				if derr == nil {
					total += size
					dirTop.push(item{Path: full, Size: size})
				} else if !isIgnorable(derr) {
					atomic.AddInt64(&s.errors, 1)
				}
			}
			continue
		}

		if info.Mode().IsRegular() {
			fs := info.Size()
			total += fs
			atomic.AddInt64(&s.filesSeen, 1)
			fileTop.push(item{Path: full, Size: fs})
		}
	}

	wg.Wait()
	return total, nil
}

func detectWindowsDrives() []string {
	var roots []string
	for c := 'A'; c <= 'Z'; c++ {
		root := string([]rune{c, ':'}) + `\`
		if _, err := os.Stat(root); err == nil {
			roots = append(roots, root)
		}
	}
	return roots
}

func isIgnorable(err error) bool {
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	// You can extend this with Windows-specific sharing violations if needed.
	return false
}

func shouldSkipByGlob(path string, patterns []string) bool {
	for _, p := range patterns {
		ok, _ := filepath.Match(p, path)
		if ok {
			return true
		}
	}
	return false
}
