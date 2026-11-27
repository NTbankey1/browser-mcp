#!/bin/sh
# Runs the browser suite: builds the development extension and the server, then
# drives them in headless Chromium. Screenshots, daemon logs and results.json go
# to apps/extension/.output/smoke/.
#
# Pass a path to run against another development build:
#   tests/browser/run.sh --extension /path/to/chrome-mv3-dev
set -e
cd "$(dirname "$0")/../.."
pnpm --filter @browsermcp/extension smoke "$@"
