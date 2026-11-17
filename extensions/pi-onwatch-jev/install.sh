#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PI_AGENT_DIR="${PI_AGENT_DIR:-$HOME/.pi/agent}"
PI_EXTENSIONS_DIR="$PI_AGENT_DIR/extensions"
TARGET_LINK="$PI_EXTENSIONS_DIR/pi-onwatch-jev"
LEGACY_FILE="$PI_EXTENSIONS_DIR/openrouter-presets.ts"
LEGACY_BACKUP="$PI_AGENT_DIR/openrouter-presets.ts.bak"

DRY_RUN=0
UNINSTALL=0

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --uninstall) UNINSTALL=1 ;;
    -h|--help)
      cat <<EOF
Usage: $(basename "$0") [--dry-run] [--uninstall]

Installs pi-onwatch-jev into ~/.pi/agent/extensions/ by symlinking
$SCRIPT_DIR and backing up openrouter-presets.ts.

Options:
  --dry-run    Print planned filesystem changes without modifying anything
  --uninstall  Remove the pi-onwatch-jev symlink and restore openrouter-presets.ts if backed up
EOF
      exit 0
      ;;
    *)
      echo "Unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "[dry-run] $*"
  else
    "$@"
  fi
}

if [ "$UNINSTALL" -eq 1 ]; then
  if [ -L "$TARGET_LINK" ] || [ -e "$TARGET_LINK" ]; then
    echo "Removing $TARGET_LINK"
    run rm -rf "$TARGET_LINK"
  else
    echo "Not installed at $TARGET_LINK"
  fi
  if [ -f "$LEGACY_BACKUP" ] && [ ! -e "$LEGACY_FILE" ]; then
    echo "Restoring $LEGACY_BACKUP -> $LEGACY_FILE"
    run mv "$LEGACY_BACKUP" "$LEGACY_FILE"
  fi
  exit 0
fi

if [ ! -f "$SCRIPT_DIR/index.ts" ]; then
  echo "Error: $SCRIPT_DIR/index.ts not found." >&2
  exit 1
fi

run mkdir -p "$PI_EXTENSIONS_DIR"

if [ -e "$LEGACY_FILE" ] || [ -L "$LEGACY_FILE" ]; then
  if [ -e "$LEGACY_BACKUP" ]; then
    ts="$(date +%Y%m%d-%H%M%S)"
    LEGACY_BACKUP="$PI_AGENT_DIR/openrouter-presets.ts.$ts.bak"
  fi
  echo "Backing up legacy $LEGACY_FILE -> $LEGACY_BACKUP"
  run mv "$LEGACY_FILE" "$LEGACY_BACKUP"
fi

if [ -L "$TARGET_LINK" ]; then
  current_target="$(readlink "$TARGET_LINK")"
  if [ "$current_target" = "$SCRIPT_DIR" ]; then
    echo "Symlink already points to $SCRIPT_DIR"
  else
    echo "Updating symlink $TARGET_LINK -> $SCRIPT_DIR (was $current_target)"
    run rm -f "$TARGET_LINK"
    run ln -s "$SCRIPT_DIR" "$TARGET_LINK"
  fi
elif [ -e "$TARGET_LINK" ]; then
  echo "Error: $TARGET_LINK already exists and is not a symlink. Move or remove it first." >&2
  exit 1
else
  echo "Creating symlink $TARGET_LINK -> $SCRIPT_DIR"
  run ln -s "$SCRIPT_DIR" "$TARGET_LINK"
fi

echo ""
echo "Installed pi-onwatch-jev."
echo "  Extension: $TARGET_LINK -> $SCRIPT_DIR"
if [ -f "$PI_AGENT_DIR/auth.json" ]; then
  echo "  Pi auth:   $PI_AGENT_DIR/auth.json found (OpenRouter key is reused automatically for Jev SystemOne)"
else
  echo "  Pi auth:   $PI_AGENT_DIR/auth.json not found; set OPENROUTER_API_KEY or run /login openrouter in Pi"
fi
if [ -f "$HOME/.onwatch/port" ]; then
  echo "  onWatch:   detected port file $HOME/.onwatch/port (port $(tr -d '[:space:]' < "$HOME/.onwatch/port"))"
else
  echo "  onWatch:   no ~/.onwatch/port found (will try ONWATCH_PORT or http://127.0.0.1:9211)"
fi
