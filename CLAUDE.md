# CLAUDE.md - Fotobank Development Guide

## Project Overview

Fotobank is a personal photo management system designed to give photographers complete control over their photo libraries without dependence on cloud services. It provides deduplication, consistent file organization, and a metadata registry while coexisting with tools like Adobe Lightroom Classic.

**Philosophy**: Your photos are irreplaceable. Fotobank aims to be the foundation for a self-hosted, future-proof photo archive that you control completely.

## Quick Reference

```bash
# Install dependencies
uv sync

# Run CLI commands
uv run fotobank import -D ~/photo_archive ~/new_photos
uv run fotobank sync-metadata -D ~/photo_archive

# Development
uv run ipython              # Interactive shell
uv run flake8 fotobank/     # Lint code
```

## Architecture

```
fotobank/
├── cli.py       # Click CLI entry points (import, sync-metadata)
├── common.py    # Domain models (Photo, ImageMetadata, BasePhoto)
├── store.py     # PhotoStore class - database & file operations
└── util.py      # Utilities (checksum, file discovery, path helpers)
```

**Data Flow**: CLI -> PhotoStore -> (SQLite registry + filesystem)

**EXIF Extraction**: Uses `exifread` (pure Python) with automatic fallback to `exiftool` if installed.

## Database Schema

SQLite database at `{base_path}/registry.sqlite` with single `photos` table:

| Column | Type | Description |
|--------|------|-------------|
| path | String | Relative path in store |
| original_filename | String | Source path before import |
| timestamp | DateTime | Photo creation date (EXIF) |
| size | Integer | File size in bytes |
| checksum | String | MD5 hash (primary dedup key) |
| make/model | String | Camera info |
| width/height | Integer | Dimensions |
| focal_length | String | Lens focal length |
| iso | Integer | ISO sensitivity |
| shutter | String | Shutter speed |
| aperture | Float | F-number |

## File Organization

```
{base_path}/
├── registry.sqlite          # Metadata database
├── movies/                  # Videos stored as {md5}.{ext}
├── 2020/                    # Year-based photo directories
│   ├── 20200615_143022_0.jpg
│   └── 20200615_143022_1.jpg  # Sequence for same-second
├── 2021/
└── unknown_date/            # Photos without valid timestamp
```

**Naming Convention**: `YYYYMMDD_HHMMSS_{seq}.{ext}`

## Supported Formats

- **Photos** (EXIF extracted): JPG, JPEG, GIF, ARW, RAF, DNG, CR2
- **Movies** (checksum only): MP4, AVI, MOV, MP2, MPG

## Key Code Patterns

### EXIF Extraction
Two-tier extraction strategy in `common.py`:
1. **Primary**: `exifread` library (pure Python, no external dependencies)
2. **Fallback**: `exiftool` subprocess (if exifread fails and exiftool is installed)

The fallback is automatic and transparent. Both implementations handle multiple field name variants (e.g., `ShutterSpeed` vs `ShutterSpeedValue`, `DateTimeOriginal` vs `CreateDate`).

Key functions:
- `read_exif(path)` - Main entry point, handles fallback logic
- `_read_exif_exifread(path)` - Pure Python implementation
- `_read_exif_exiftool(path)` - Subprocess fallback
- `ExifReadError` - Custom exception for EXIF failures

### Deduplication
MD5 checksum-based. Photos already in registry (by checksum) are skipped with optional verbose logging.

### Property-based Metadata Access
`BasePhoto` uses `_get_metadata_field()` to dynamically access EXIF data stored in `ImageMetadata` container.

## CLI Commands

### `fotobank import <directory>`
Recursively imports photos/videos from source directory.

Options:
- `-D, --database PATH`: Store location (or set `FOTOBANK_DEFAULT_PATH`)
- `-d, --dry-run`: Preview without changes
- `-m, --move`: Move files instead of copying
- `-v, --verbose`: Show duplicate messages

### `fotobank sync-metadata`
Removes database entries for files no longer on disk.

Options:
- `-D, --database PATH`: Store location
- `-d, --dry-run`: Preview deletions

## Development Notes

### Testing
No formal test suite exists. `MockPhoto` class available for manual testing. The `script.py` file contains ad-hoc development experiments.

### Error Handling
- Missing EXIF fields return sensible defaults ('unknown', None)
- Permission errors during sync are caught and logged
- Directory listing is cached for performance on network storage

### Recent Changes
- Switched EXIF extraction to `exifread` (pure Python) with `exiftool` fallback
- Migrated to `uv` for dependency management
- Fixed SQLAlchemy deprecated syntax
- Added directory caching for sync_metadata performance
- Using `copyfile` instead of `copy` to not preserve permissions

## Vision & Roadmap

Fotobank aspires to be a complete self-hosted photo management platform for amateur photographers who want:

1. **Complete data ownership** - No cloud vendor lock-in
2. **Reliable backups** - Never lose 20 years of memories
3. **Rich browsing experience** - View and organize without Lightroom
4. **Future-proof storage** - Work with any NAS or storage solution

### Planned Features

**Near-term**:
- [ ] Transactional metadata changes with undo capability
- [ ] Database merging (combine multiple archives)
- [ ] Database splitting (cold storage for old photos)
- [ ] Thumbnail generation for fast browsing

**Gallery Web App**:
- [ ] FastAPI/Flask backend serving photo metadata
- [ ] React/Vue frontend for browsing by date, camera, location
- [ ] Lazy-loading image grid with virtual scrolling
- [ ] EXIF-based filtering and search
- [ ] Album/collection support

**Backup & Sync**:
- [ ] rsync wrapper for NAS backup with verification
- [ ] Backup manifest tracking (what's backed up where)
- [ ] Multi-destination sync (local NAS + offsite)
- [ ] Integrity checking (detect bit rot)
- [ ] Incremental backup reports

**Advanced Features**:
- [ ] Face detection/recognition (local ML models)
- [ ] Location extraction and map view
- [ ] Duplicate detection beyond checksum (perceptual hashing)
- [ ] RAW + JPEG pairing
- [ ] Lightroom catalog sync/import
- [ ] Mobile app for on-the-go access

## Integration with Lightroom

Fotobank is designed to coexist with Lightroom Classic:
1. Import photos to Fotobank archive
2. Point Lightroom's watched folder at the Fotobank base path
3. Lightroom detects new imports and adds to catalog
4. Both tools see the same organized file structure

## Contributing

When adding features:
- Keep modules focused (cli, store, common, util pattern)
- Use Click for new CLI commands
- Add to SQLAlchemy schema if storing new metadata
- Handle missing EXIF fields gracefully
- Support dry-run mode for destructive operations
- Cache filesystem operations when iterating over network storage
