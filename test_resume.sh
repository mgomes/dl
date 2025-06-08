#!/bin/bash

# Test script for auto-resume functionality

echo "=== Testing dl auto-resume functionality ==="
echo

# Test URL - using a larger file to test interruption
TEST_URL="http://speedtest.tele2.net/10MB.zip"
TEST_FILE="10MB.zip"

# Clean up any existing files
rm -f "$TEST_FILE" ".$TEST_FILE.dl_progress"

echo "1. Starting download with 8 parallel connections..."
./dl -boost 8 "$TEST_URL" &
DL_PID=$!

# Let it download for a few seconds
sleep 3

echo
echo "2. Interrupting download (Ctrl+C simulation)..."
kill -INT $DL_PID
wait $DL_PID 2>/dev/null

echo
echo "3. Checking progress file..."
if [ -f ".$TEST_FILE.dl_progress" ]; then
    echo "✓ Progress file exists"
    echo "Progress content preview:"
    cat ".$TEST_FILE.dl_progress" | jq -r '. | {version, file_size, parts: (.parts | length), created, last_updated}' 2>/dev/null || cat ".$TEST_FILE.dl_progress"
else
    echo "✗ Progress file not found"
fi

echo
echo "4. Checking partial file..."
if [ -f "$TEST_FILE" ]; then
    SIZE=$(ls -lh "$TEST_FILE" | awk '{print $5}')
    echo "✓ Partial file exists (size: $SIZE)"
else
    echo "✗ Partial file not found"
fi

echo
echo "5. Resuming download..."
./dl -boost 8 "$TEST_URL"

echo
echo "6. Verifying completion..."
if [ -f "$TEST_FILE" ]; then
    SIZE=$(ls -lh "$TEST_FILE" | awk '{print $5}')
    echo "✓ File downloaded successfully (size: $SIZE)"
else
    echo "✗ File not found"
fi

if [ ! -f ".$TEST_FILE.dl_progress" ]; then
    echo "✓ Progress file cleaned up after completion"
else
    echo "✗ Progress file still exists"
fi

echo
echo "=== Test complete ==="

# Clean up
rm -f "$TEST_FILE" ".$TEST_FILE.dl_progress"