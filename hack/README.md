# Hack Scripts

This folder contains auxiliary scripts for development and project maintenance.

## generate_release_notes.py

Script for automatic generation of release notes files from changelog files.

### Description

The script parses YAML files from the `CHANGELOG/` folder and creates two markdown files:

- `docs/RELEASE_NOTES.md` - English version of release notes
- `docs/RELEASE_NOTES.ru.md` - Russian version of release notes

### Usage

```bash
# From project root folder
python3 hack/generate_release_notes.py
```

### Requirements

- Python 3.6+
- PyYAML (`pip install PyYAML`)
- packaging (`pip install packaging`)

### Input File Format

The script expects YAML files in the `CHANGELOG/` folder with the following structure:

**English files** (e.g., `0.2.4.yml`):

```yaml
Changes:
  - Added additional mountings for containerd v2 support
  - Fixed security issue in authentication
```

**Russian files** (e.g., `0.2.4.ru.yml`):

```yaml
Изменения:
  - Добавлена дополнительные монтирования для поддержки containerd v2
  - Исправлена проблема безопасности в аутентификации
```

### Output File Format

The script creates markdown files in the following format:

```markdown
---
title: "Release Notes"
---

## v0.2.4

* Added additional mountings for containerd v2 support
* Fixed security issue in authentication

## v0.2.3

* Previous release changes...
```

### Features

- The script automatically removes existing `docs/RELEASE_NOTES.md` and `docs/RELEASE_NOTES.ru.md` files before creating new ones
- Versions are sorted by semantic versioning (e.g., v0.1.10 comes after v0.1.2)
- Versions are displayed in reverse order (newest versions first)
- Both list of changes and single change are supported
- The script handles errors and outputs informative messages

## git_commits_after_tag.py

Script for getting a list of commits that are not included in the latest tag.

### Description

The script performs the following actions:
1. Switches to the main branch
2. Executes git fetch to get updates
3. Finds the latest tag in the repository
4. Displays a list of commits after the latest tag

### Usage

```bash
# From project root folder
python3 hack/git_commits_after_tag.py
```

### Requirements

- Python 3.6+
- Git repository

### Functionality

- **Automatic switch to main**: The script automatically switches to the main branch
- **Getting updates**: Executes `git fetch --all` to get the latest changes
- **Finding latest tag**: Finds the newest tag in the repository
- **List of commits**: Shows all commits after the latest tag
- **Detailed information**: Displays change statistics for each commit
- **Error handling**: Informative error messages

### Example Output

```
🚀 Script for getting commits after latest tag
============================================================
📍 Current branch: main
🔄 Switching to main branch...
✅ Successfully switched to main branch
🔄 Getting updates (git fetch)...
✅ Updates received successfully
🔍 Searching for latest tag...
✅ Latest tag: v0.2.4
🔍 Searching for commits after tag v0.2.4...
✅ Found 2 commits after tag v0.2.4

📋 List of commits after latest tag (2 commits):
============================================================
 1. a1b2c3d - feat: add new feature
 2. e4f5g6h - fix: resolve bug in authentication
============================================================
```

### Features

- Works with any git repositories
- Automatically handles absence of tags
- Shows detailed information about the first 10 commits
- Excludes merge commits from the list
- Supports colored output with emojis for better readability
