#!/bin/sh
# Builds the macOS application.
#
#   ./build.sh        builds it and installs /Applications/MusicLibraryOrganizer.app
#   ./build.sh run    the same, then opens it
#   ./build.sh zip    dist/MusicLibraryOrganizer-<version>-macos-universal.zip,
#                     for a release
#
# The build happens in a copy of the project in the temporary folder; the
# project itself gets no build folder.
set -e
cd "$(dirname "$0")"

PATH="$(go env GOPATH)/bin:$PATH"
export PATH

if ! command -v wails >/dev/null 2>&1; then
	echo "Installing the Wails CLI..."
	go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
fi

case "$1" in
zip) make mac-zip ;;
run) make mac && open "$(make -s -f Makefile print-installed)" ;;
*)   make mac ;;
esac
