#!/bin/bash

# Test script for Proxy Switcher

set -e

echo "=== Proxy Switcher Test Suite ==="
echo ""

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Test counter
TESTS=0
PASSED=0
FAILED=0

run_test() {
    TESTS=$((TESTS + 1))
    echo -n "Running: $1... "
    if eval "$2"; then
        echo -e "${GREEN}PASSED${NC}"
        PASSED=$((PASSED + 1))
        return 0
    else
        echo -e "${RED}FAILED${NC}"
        FAILED=$((FAILED + 1))
        return 1
    fi
}

# Test 1: Build
run_test "Build application" "GOCACHE=/tmp/go-build GOPATH=/tmp/go go build -o /tmp/proxy-switcher ./cmd/"

# Test 2: Unit tests
run_test "Unit tests" "GOCACHE=/tmp/go-build GOPATH=/tmp/go go test ./internal/..."

# Test 3: Config validation
run_test "Config file exists" "test -f configs/config.example.yaml"

# Test 4: Dockerfile exists
run_test "Dockerfile exists" "test -f Dockerfile"

# Test 5: docker-compose exists
run_test "docker-compose.yml exists" "test -f docker-compose.yml"

# Test 6: README exists
run_test "README.md exists" "test -f README.md"

# Cleanup
rm -f /tmp/proxy-switcher

echo ""
echo "=== Test Summary ==="
echo "Total:  $TESTS"
echo -e "Passed: ${GREEN}$PASSED${NC}"
echo -e "Failed: ${RED}$FAILED${NC}"

if [ $FAILED -eq 0 ]; then
    echo ""
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
else
    echo ""
    echo -e "${RED}Some tests failed!${NC}"
    exit 1
fi
