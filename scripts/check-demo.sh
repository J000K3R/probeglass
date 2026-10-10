#!/bin/sh
# Check that the published demo is the demo this tree would write.
#
# The demo on the website is not maintained by hand. It is produced by
# `cmd/demogen` out of the dashboard the daemon serves, and this script is what
# keeps that true: it regenerates the demo into a temporary directory and compares
# the result against the copy that is committed. Any difference means the website is
# serving yesterday's dashboard — a missing widget, an old stylesheet, a route the
# interface calls and the recording does not answer — and that is exactly the failure
# this project cannot afford, because the whole claim of the demo is that it is the
# product.
#
#   usage: scripts/check-demo.sh [committed-demo-directory]
#
# It is run by `make demo-check` and by the workflow in the website repository, which
# checks out this repository beside its own and calls it. If it fails, run `make demo`
# in the program tree and commit what changed.
#
# Two comparisons, for two different reasons:
#
#   * the fingerprint, which covers the shape of every recorded answer as well as the
#     files, and is therefore the one that notices a renamed field;
#   * a byte comparison of the dashboard's own files and the shell, which is the claim
#     "the demo is the dashboard" stated literally.
#
# The recorded answers themselves are not compared byte for byte on purpose. They
# describe a week that ends now: the timestamps and the aggregates move with the
# moment the recording is made, and a byte comparison would report a difference on
# every run until somebody stopped reading it.
set -eu

demo=${1:-../weatherglass-website/site/demo}

if [ ! -f "$demo/fingerprint.txt" ]; then
	echo "check-demo: $demo/fingerprint.txt is missing." >&2
	echo "check-demo: that is the published demo, or the wrong directory was passed." >&2
	exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

go run ./cmd/demogen -out "$work/demo"

status=0

want=$(cat "$demo/fingerprint.txt")
got=$(cat "$work/demo/fingerprint.txt")
if [ "$want" != "$got" ]; then
	echo "check-demo: the published demo is out of date." >&2
	echo "check-demo:   committed  $want" >&2
	echo "check-demo:   this tree  $got" >&2
	status=1
fi

if ! diff -r "$demo/assets" "$work/demo/assets"; then
	echo "check-demo: the dashboard files in the demo are not the ones in this tree." >&2
	status=1
fi

if ! diff -u "$demo/index.html" "$work/demo/index.html"; then
	echo "check-demo: the demo's page is not the one this tree writes." >&2
	status=1
fi

if [ "$status" -ne 0 ]; then
	echo "check-demo: run 'make demo' and commit what changed." >&2
	exit 1
fi

echo "check-demo: the published demo is current ($want)"
