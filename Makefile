# The agent is embedded into the application, so it is always built first:
# an application built against a stale agent reads tags on the phone with old
# code, and files the desktop handles fine start failing there.

AGENT := internal/device/agent/mlm-agent-arm64
VERSION := $(shell sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' wails.json)
MAC_ZIP := dist/MusicLibraryOrganizer-$(VERSION)-macos-universal.zip

# What Wails packs into the application — the icon, Info.plist, the Windows
# manifest — lives in packaging/. Wails wants it in a build/ folder, which
# `make assets` fills, and puts what it builds there too.
#
# A Mac build never makes that folder in the project. The project lives in
# iCloud Drive, which gives every file it syncs Finder attributes that codesign
# refuses, and would sync a built bundle as a pile of folders besides. So the
# project is copied to a temporary folder, built and signed there, and the
# application goes straight to Applications (or into the release zip).
STAGE := $(shell echo "$${TMPDIR:-/tmp}" | sed 's:/*$$::')/music-library-organizer-build
STAGED_APP := $(STAGE)/build/bin/MusicLibraryOrganizer.app
APPLICATIONS := $(shell [ -w /Applications ] && echo /Applications || echo "$$HOME/Applications")
INSTALLED := $(APPLICATIONS)/MusicLibraryOrganizer.app

.PHONY: build assets agent mac mac-build mac-zip icon test dev clean print-installed

assets:
	mkdir -p build
	cp -R packaging/. build/

build: agent assets
	wails build

# One application for Apple Silicon and Intel alike, built and signed ad hoc
# in the temporary copy.
mac-build: agent
	rm -rf "$(STAGE)"
	mkdir -p "$(STAGE)"
	rsync -a --exclude /.git --exclude /build --exclude /dist --exclude "/Music Library Organizer.exe" ./ "$(STAGE)/"
	test -f "$(STAGE)/frontend/dist/index.html"
	mkdir -p "$(STAGE)/build"
	cp -R packaging/. "$(STAGE)/build/"
	cd "$(STAGE)" && wails build -platform darwin/universal
	codesign --verify --deep --strict "$(STAGED_APP)"

# Builds the application and installs it in Applications.
mac: mac-build
	mkdir -p "$(APPLICATIONS)"
	rm -rf "$(INSTALLED)"
	ditto "$(STAGED_APP)" "$(INSTALLED)"
	codesign --verify --deep --strict "$(INSTALLED)"
	rm -rf "$(STAGE)"
	@echo "Installed $(INSTALLED) ($(VERSION))"

# The download for a release.
mac-zip: mac-build
	mkdir -p dist
	ditto -c -k --sequesterRsrc --keepParent "$(STAGED_APP)" "$(MAC_ZIP)"
	rm -rf "$(STAGE)"
	@echo "Built $(MAC_ZIP)"

agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(AGENT) ./cmd/agent

# The icon is drawn with AppKit, so only on a Mac.
icon:
	clang -fobjc-arc -framework Cocoa tools/icon/make_icon.m -o "$(STAGE)-icon"
	"$(STAGE)-icon" packaging
	rm -f "$(STAGE)-icon"

test: agent
	go test ./...

dev: agent assets
	wails dev

clean:
	rm -rf build dist "$(STAGE)"

print-installed:
	@echo "$(INSTALLED)"
