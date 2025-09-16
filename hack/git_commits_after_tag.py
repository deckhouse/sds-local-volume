#!/usr/bin/env python3

# Copyright 2025 Flant JSC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
# 
#     http://www.apache.org/licenses/LICENSE-2.0
# 
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# run from repository root

"""
Script for getting a list of commits that are not included in the latest tag.

The script performs the following actions:
1. Switches to the main branch
2. Executes git fetch to get updates
3. Finds the latest tag
4. Displays a list of commits after the latest tag
"""

import subprocess
import sys
import re
from typing import List, Optional, Tuple


def run_git_command(command: List[str], cwd: str = None) -> Tuple[bool, str]:
    """Executes git command and returns result."""
    try:
        result = subprocess.run(
            command,
            cwd=cwd,
            capture_output=True,
            text=True,
            check=True
        )
        return True, result.stdout.strip()
    except subprocess.CalledProcessError as e:
        return False, e.stderr.strip()


def get_current_branch() -> Optional[str]:
    """Gets current branch."""
    success, output = run_git_command(["git", "branch", "--show-current"])
    if success and output:
        return output
    return None


def switch_to_main() -> bool:
    """Switches to main branch."""
    print("🔄 Switching to main branch...")
    
    # Check if main branch exists
    success, _ = run_git_command(["git", "show-ref", "--verify", "--quiet", "refs/heads/main"])
    if not success:
        # Try origin/main
        success, _ = run_git_command(["git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/main"])
        if not success:
            print("❌ Main branch not found")
            return False
    
    # Switch to main
    success, error = run_git_command(["git", "checkout", "main"])
    if not success:
        print(f"❌ Error switching to main: {error}")
        return False
    
    print("✅ Successfully switched to main branch")
    return True


def fetch_updates() -> bool:
    """Executes git fetch to get updates."""
    print("🔄 Getting updates (git fetch)...")
    
    success, error = run_git_command(["git", "fetch", "--all"])
    if not success:
        print(f"❌ Error executing git fetch: {error}")
        return False
    
    print("✅ Updates received successfully")
    return True


def get_latest_tag() -> Optional[str]:
    """Gets the latest tag in the repository."""
    print("🔍 Searching for latest tag...")
    
    # Get all tags sorted by date
    success, output = run_git_command([
        "git", "tag", "--sort=-version:refname", "--merged"
    ])
    
    if not success or not output:
        print("⚠️  No tags found")
        return None
    
    # Take the first tag (newest)
    tags = output.split('\n')
    latest_tag = tags[0].strip()
    
    print(f"✅ Latest tag: {latest_tag}")
    return latest_tag


def get_commits_after_tag(tag: str) -> List[str]:
    """Gets list of commits after the specified tag."""
    print(f"🔍 Searching for commits after tag {tag}...")
    
    # Get commits after tag
    success, output = run_git_command([
        "git", "log", f"{tag}..HEAD", "--oneline", "--no-merges"
    ])
    
    if not success:
        print(f"❌ Error getting commits: {output}")
        return []
    
    if not output:
        print("✅ No commits found after latest tag")
        return []
    
    commits = [line.strip() for line in output.split('\n') if line.strip()]
    print(f"✅ Found {len(commits)} commits after tag {tag}")
    
    return commits


def format_commit_info(commits: List[str]) -> str:
    """Formats commit information for output."""
    if not commits:
        return "No commits found after latest tag."
    
    result = []
    result.append(f"\n📋 List of commits after latest tag ({len(commits)} commits):")
    result.append("=" * 60)
    
    for i, commit in enumerate(commits, 1):
        # Parse commit: hash and message
        parts = commit.split(' ', 1)
        if len(parts) == 2:
            commit_hash = parts[0]
            message = parts[1]
            result.append(f"{i:2d}. {commit_hash[:8]} - {message}")
        else:
            result.append(f"{i:2d}. {commit}")
    
    result.append("=" * 60)
    return "\n".join(result)


def get_commit_details(commits: List[str]) -> str:
    """Gets detailed information about commits."""
    if not commits:
        return ""
    
    result = []
    result.append("\n📊 Detailed commit information:")
    result.append("=" * 60)
    
    for i, commit in enumerate(commits[:10], 1):  # Show only first 10
        commit_hash = commit.split(' ')[0]
        
        # Get detailed commit information
        success, details = run_git_command([
            "git", "show", "--stat", "--no-patch", commit_hash
        ])
        
        if success:
            lines = details.split('\n')
            commit_info = lines[0] if lines else commit
            result.append(f"\n{i}. {commit_info}")
            
            # Add change statistics
            for line in lines[1:]:
                if line.strip() and ('file' in line.lower() or 'insertion' in line.lower() or 'deletion' in line.lower()):
                    result.append(f"   {line.strip()}")
    
    if len(commits) > 10:
        result.append(f"\n... and {len(commits) - 10} more commits")
    
    result.append("=" * 60)
    return "\n".join(result)


def main():
    """Main script function."""
    print("🚀 Script for getting commits after latest tag")
    print("=" * 60)
    
    # Check that we are in a git repository
    success, _ = run_git_command(["git", "rev-parse", "--git-dir"])
    if not success:
        print("❌ Error: current directory is not a git repository")
        return 1
    
    # Show current branch
    current_branch = get_current_branch()
    if current_branch:
        print(f"📍 Current branch: {current_branch}")
    
    # Switch to main
    if not switch_to_main():
        return 1
    
    # Get updates
    if not fetch_updates():
        return 1
    
    # Find latest tag
    latest_tag = get_latest_tag()
    if not latest_tag:
        print("⚠️  Could not find tags. Showing all commits in main branch.")
        # If no tags, show all commits in main
        success, output = run_git_command([
            "git", "log", "--oneline", "--no-merges", "-20"
        ])
        if success and output:
            commits = [line.strip() for line in output.split('\n') if line.strip()]
            print(format_commit_info(commits))
        return 0
    
    # Get commits after tag
    commits = get_commits_after_tag(latest_tag)
    
    # Output result
    print(format_commit_info(commits))
    
    # Show detailed information
    if commits:
        print(get_commit_details(commits))
    
    print("\n✅ Script completed successfully!")
    return 0


if __name__ == "__main__":
    exit(main())
