#!/bin/bash
set -e

# Create release directory if it doesn't exist
mkdir -p release

# Set version
VERSION="1.0.0"
echo "Building L2 Standalone Client version $VERSION"

# Check and install necessary tools
check_and_install_tool() {
    local tool=$1
    local install_cmd=$2
    
    if ! command -v $tool &> /dev/null; then
        echo "$tool not found. Installing..."
        eval $install_cmd
        if ! command -v $tool &> /dev/null; then
            echo "Failed to install $tool. Skipping related build steps."
            return 1
        fi
    fi
    return 0
}

# Check for fyne-cross to build Windows version
FYNE_CROSS=$HOME/go/bin/fyne-cross
if ! check_and_install_tool "$FYNE_CROSS" "go install github.com/fyne-io/fyne-cross@latest"; then
    echo "fyne-cross is required for building. Aborting."
    exit 1
fi

# Build standalone client application for Windows
echo "Building Standalone Client for Windows..."

# First, update dependencies to ensure go.sum is up to date
cd client-standalone
echo "Updating dependencies..."
go mod tidy

echo "Building using fyne-cross..."
$FYNE_CROSS windows -app-id com.l2games.client -app-version $VERSION -name "L2Client-Standalone" -icon icon.png -arch=amd64 -tags disablegles31

# Copy Windows version to release directory
if [ -f "fyne-cross/dist/windows-amd64/L2Client-Standalone.zip" ]; then
    cp fyne-cross/dist/windows-amd64/L2Client-Standalone.zip ../release/
    echo "Windows client build completed successfully."
else
    echo "Windows client build failed or output file not found."
fi

cd ..

echo "Build completed successfully!"
echo "Release files are available in the 'release' directory."

# Display information about created files
echo "Created files:"
ls -la release/
