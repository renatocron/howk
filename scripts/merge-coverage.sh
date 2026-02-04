#!/bin/bash
# Merge coverage files using gocovmerge

# Find gocovmerge - check multiple locations
GOCOVMERGE=$(which gocovmerge 2>/dev/null)

if [ -z "$GOCOVMERGE" ]; then
    # Try to find in GOPATH
    GOPATH=$(go env GOPATH)
    GOCOVMERGE=$(find ${GOPATH//:/ } -name gocovmerge -type f 2>/dev/null | head -1)
fi

if [ -z "$GOCOVMERGE" ]; then
    # Try common locations
    GOCOVMERGE=$(find ~/.asdf ~/go -name gocovmerge -type f 2>/dev/null | head -1)
fi

# Install if not found
if [ -z "$GOCOVMERGE" ] || [ ! -f "$GOCOVMERGE" ]; then
    echo "Installing gocovmerge..."
    go install github.com/wadey/gocovmerge@latest
    
    # Try to find again
    GOCOVMERGE=$(which gocovmerge 2>/dev/null)
    if [ -z "$GOCOVMERGE" ]; then
        GOPATH=$(go env GOPATH)
        GOCOVMERGE=$(find ${GOPATH//:/ } ~/.asdf ~/go -name gocovmerge -type f 2>/dev/null | head -1)
    fi
fi

if [ -z "$GOCOVMERGE" ] || [ ! -f "$GOCOVMERGE" ]; then
    echo "Error: Could not find or install gocovmerge" >&2
    exit 1
fi

# Merge coverage files
$GOCOVMERGE "$@"
