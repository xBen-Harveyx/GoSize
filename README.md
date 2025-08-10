# GoSize

[![Go Version](https://img.shields.io/github/go-mod/go-version/xBen-Harveyx/GoSize)](https://golang.org/dl/)
[![Build](https://img.shields.io/github/actions/workflow/status/xBen-Harveyx/GoSize/go.yml?label=build)](https://github.com/xBen-Harveyx/GoSize/actions)
[![License](https://img.shields.io/github/license/xBen-Harveyx/GoSize)](LICENSE)
[![Issues](https://img.shields.io/github/issues/xBen-Harveyx/GoSize)](https://github.com/xBen-Harveyx/GoSize/issues)
[![Stars](https://img.shields.io/github/stars/xBen-Harveyx/GoSize?style=social)](https://github.com/xBen-Harveyx/GoSize/stargazers)

GoSize is a fast command-line utility written in Go that scans a Windows system and reports the largest directories and files on each drive.  
It’s designed for quick disk space analysis so you can identify space hogs for cleanup.


## Features
- Scans one or more drives or directories concurrently
- Shows both largest **directories** and **files**
- Displays size in human-readable units
- New: **DRIVE%** column shows how much of the total drive space a file/dir consumes
- Optional skip filters (hidden files, glob patterns, symlinks)
- Progress reporting during long scans


## Installation
```powershell
# Clone the repo
git clone https://github.com/YourUser/GoSize.git
cd GoSize

# Build
go get golang.org/x/sys/windows
go build -o gosize.exe

gosize.exe [options]
```

| Flag           | Description                                                     |
| -------------- | --------------------------------------------------------------- |
| `-top`         | Number of largest files/dirs to keep in each list (default: 20) |
| `-workers`     | Number of concurrent directory workers (default: CPU count)     |
| `-roots`       | Comma-separated roots to scan (default: all detected drives)    |
| `-followlinks` | Follow symlinks/junctions                                       |
| `-maxdepth`    | Limit directory depth (0 = unlimited)                           |
| `-skiphidden`  | Skip hidden files/dirs (dot-prefix)                             |
| `-skip`        | Comma-separated glob patterns to skip                           |
| `-progress`    | Show progress every 2s (default: true)                          |
| `-json`	     | Output results as JSON instead of tables                        |

### Example Run:
```PowerShell
.\gosize.exe -top=5 -workers=8 -progress -roots="C:\"
```

### Sample Output
```
Largest Directories
RANK  SIZE      DRIVE%   PATH
1     42.37 GB  8.47%    C:\Users\John\AppData\Local\Temp
2     38.21 GB  7.64%    C:\Program Files\BigApp
3     21.03 GB  4.20%    C:\Windows\Installer
4     19.82 GB  3.96%    C:\Games
5     15.77 GB  3.15%    C:\Users\John\Videos

Largest Files
RANK  SIZE      DRIVE%   PATH
1     8.02 GB   1.60%    C:\Games\bigfile.pak
2     6.45 GB   1.29%    C:\Users\John\Videos\movie.mkv
3     4.10 GB   0.82%    C:\Program Files\BigApp\data.bin
4     3.85 GB   0.77%    C:\Windows\Installer\setup.msi
5     2.71 GB   0.54%    C:\Users\John\Downloads\iso.img

Scanned 481,532 files in 92,418 directories in 42.236s (skipped=23, errors=14)
```

### JSON Output

```PowerShell
.\gosize.exe -roots="C:\" -top=3 -json > report.json
```

```json
{
  "roots": ["C:\\"],
  "topK": 3,
  "generated": "2025-08-10T15:21:12-04:00",
  "duration": "42.236s",
  "summary": {
    "filesSeen": 481532,
    "dirsSeen": 92418,
    "skipped": 23,
    "errors": 14
  },
  "directories": [
    {
      "rank": 1,
      "sizeBytes": 45475745792,
      "sizeHuman": "42.37 GB",
      "drivePercent": 8.47,
      "drive": "C:\\",
      "path": "C:\\Users\\John\\AppData\\Local\\Temp"
    }
  ],
  "files": [
    {
      "rank": 1,
      "sizeBytes": 8600938496,
      "sizeHuman": "8.02 GB",
      "drivePercent": 1.60,
      "drive": "C:\\",
      "path": "C:\\Games\\bigfile.pak"
    }
  ]
}
```

## How It Works (High-Level)
1. **Flag Parsing** – The program reads CLI flags to decide what to scan, how deep to go, and what to skip.
2. **Root Detection** – If no -roots are specified, it auto-detects all Windows drives (A:\ to Z:\ that exist).
3. **Concurrent Walk** – It recursively walks each directory tree using a semaphore to limit concurrency.
4. **Filtering** – Skips entries based on symlink settings, skip patterns, or hidden flag (if enabled).
5. **Top-K Tracking** – Maintains min-heaps for the largest files and largest directories.
6. **Drive Size Lookup** – Uses the Windows API to get total drive capacity for the DRIVE% calculation.
7. **Output** – Prints either:
    - Two sorted tables (directories, files) plus a summary, OR
    - A structured JSON document if -json is set.
