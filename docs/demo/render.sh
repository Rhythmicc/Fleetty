#!/bin/sh
# Regenerate the README screenshots from the real TUI.
#
#   sh docs/demo/render.sh
#
# Builds Fleetty with -tags demo, renders the hub, GPU node, and storage node
# through View(), and rasterizes those frames. Requires Go, Python 3, Pillow,
# JetBrains Mono, and ffmpeg.

set -eu

root=$(unset CDPATH && cd "$(dirname "$0")/../.." && pwd)
cd "${root}"

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/fleetty-demo.XXXXXX")
cleanup() {
  rm -rf "${tmpdir}"
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

go build -tags demo -o "${tmpdir}/fleetty" ./cmd/fleetty

render_png() {
  screen=$1
  theme=$2
  png=$3
  "${tmpdir}/fleetty" demo --screen "${screen}" --theme "${theme}" > "${tmpdir}/${screen}-${theme}.ansi"
  python3 "${root}/docs/demo/rasterize.py" --theme "${theme}" \
    "${tmpdir}/${screen}-${theme}.ansi" "${png}"
}

to_webp() {
  ffmpeg -y -loglevel error -i "$1" -c:v libwebp -quality 82 -compression_level 6 "$2"
}

render_png hub dark "${tmpdir}/hub-dark.png"
render_png hub light "${tmpdir}/hub-light.png"
render_png gpu dark "${tmpdir}/gpu-dark.png"
render_png nas dark "${tmpdir}/nas-dark.png"

mkdir -p "${root}/docs/images"
to_webp "${tmpdir}/hub-dark.png" "${root}/docs/images/fleetty-hub.webp"
to_webp "${tmpdir}/hub-light.png" "${root}/docs/images/fleetty-hub-light.webp"
to_webp "${tmpdir}/gpu-dark.png" "${root}/docs/images/fleetty-gpu.webp"
to_webp "${tmpdir}/nas-dark.png" "${root}/docs/images/fleetty-nas.webp"

# Two holds of the same hub, dark then light. GitHub animates GIF.
ffmpeg -y -loglevel error \
  -loop 1 -t 1.6 -i "${tmpdir}/hub-dark.png" \
  -loop 1 -t 1.6 -i "${tmpdir}/hub-light.png" \
  -filter_complex "[0:v][1:v]concat=n=2:v=1:a=0,split[v1][v2];[v1]palettegen=stats_mode=diff:max_colors=128[p];[v2][p]paletteuse=dither=bayer" \
  -loop 0 "${root}/docs/images/fleetty-themes.gif"

printf 'Wrote docs/images/fleetty-hub.webp\n'
printf 'Wrote docs/images/fleetty-hub-light.webp\n'
printf 'Wrote docs/images/fleetty-gpu.webp\n'
printf 'Wrote docs/images/fleetty-nas.webp\n'
printf 'Wrote docs/images/fleetty-themes.gif\n'
