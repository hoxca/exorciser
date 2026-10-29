# exorciser

exorciser finds faint ghost artefacts in astronomical images: the soft, diffuse
reflections and stray-light patches that survive stacking and are hard to see
by eye. It takes star-aligned light frames, uses one visible artefact as a
starting point, measures it in every frame, and then catalogues every other
artefact of the same kind in the image.

The result is a list of positions and a set of annotated images you can open in
PixInsight.

```
detect  ->  scan  ->  annotate  ->  union  ->  erase
```

## Installation

exorciser is a single executable. It does not install anything into
PixInsight, and it never modifies your input frames.

### Prebuilt executable

Copy `exorciser` (Windows: `exorciser.exe`) to a directory of your choice, for
example `~/bin`, and make it executable:

```
chmod +x ~/bin/exorciser
```

If that directory is on your `PATH`, run it from anywhere. Otherwise call it by
its full path.

### Building from source

You need Go 1.22 or newer and `make`. From the project directory:

```
make build
```

The program is written to `bin/exorciser`. Check it starts:

```
./bin/exorciser --help
```

## Before you start

exorciser works on the frames you would stack, before stretching:

- Calibrated light frames in `.xisf` format.
- Star-aligned (registered), so the same star sits at the same pixel in every
  frame.
- Linear data, all frames the same size.
- Grouped by filter in directories whose name contains the filter, for example
  `Light_BIN-1_4096x4096_EXPOSURE-600.00s_FILTER-L_mono` for L. The filter
  letters are L, R, G, B, S, H and O. `-i img/registered` picks up the
  `_FILTER-L_`, `_FILTER-R_` and other subdirectories automatically.

You also need:

- **A reference frame** from the same filter in which the artefact is absent.
  A frame from another night or another framing angle is a good choice. The
  software measures the difference between each frame and this reference, so a
  ghost present in the reference cancels and becomes invisible.
- **A frame where the artefact is clearly visible.** This is used once, to tell
  the software what the artefact looks like.
- **The pixel coordinate of the artefact.** In PixInsight, hover over its
  centre and read X and Y from the Readout. PixInsight counts from 1, while
  exorciser counts from 0, so write `x-1,y-1`. A one-pixel difference does not
  matter: the searched window is 128 pixels wide by default.

The commands below write their results under `output/`. Nothing else is
touched.

## The workflow

| Step | Runs | What it does |
| --- | --- | --- |
| `detect` | once per filter | Measures the chosen artefact in every frame and marks each frame as containing it or not. |
| `scan` | once per filter | Stacks the two groups of frames, subtracts them, and finds every artefact in the difference. |
| `annotate` | once per filter | Draws the catalogue on the stacked image so you can see it in PixInsight. |
| `union` | once, after all filters | Combines the catalogues, marks in yellow what each filter missed, and samples the background sky. |
| `erase` | once per filter | Removes the catalogued artefacts from the frames that show them and writes repaired copies. |

The commands can be typed one at a time, or run from the example scripts in
`output/cmd/`. The scripts contain the exact commands used for the L, R, G and
B data of this project and are the reference for the examples below.

## Step 1 - detect

`detect` compares every frame with the reference frame in a square window
around the artefact. A frame is marked as detected when the artefact is
significantly stronger in it than in the reference.

Examples (from `output/cmd/L_detect.sh` and `output/cmd/B_detect.sh`):

```
./bin/exorciser detect -i ./img/registered --filter L -o output -w 128 -j 8 \
  --coord 2460,1978 --dirty 007 --dirty-coord 2464,1985 \
  --reference ./img/registered/Light_BIN-1_4096x4096_EXPOSURE-600.00s_FILTER-L_mono/VDB149_LIGHT_L_600s_BIN1_002_-09C_20220821_235716_905_W_c_r.xisf \
  --ghost-sigma 4,8,16 --min-snr 5 --min-contrast 1.5
```

```
./bin/exorciser detect -i ./img/registered --filter B -o output -w 128 -j 8 \
  --coord 2474,1974 --reference 009 --dirty 013 \
  --ghost-sigma 4,8,16 --min-snr 10
```

Main options:

| Option | Meaning |
| --- | --- |
| `-i` | Directory searched for `.xisf` files. Subdirectories are included. |
| `--filter` | Which filter to process: L, R, G, B, S, H or O. |
| `--reference` | The clean frame. Either its frame number (the digits after `BIN1_`, e.g. `002` in `..._BIN1_002_...`) or the full path to the file. Required. |
| `--dirty` | Optional. A frame where the artefact is obvious. The software extracts its shape and uses it as an extra pattern to look for. |
| `--coord` | Centre of the search window in pixels, as `x,y`. |
| `--dirty-coord` | Position of the artefact in the dirty frame. Defaults to `--coord`, which is usually right because the frames are aligned. |
| `-w` | Window size in pixels. Increase it if the artefact is larger than 128 pixels. |
| `--min-snr` | How strong a detection must be. Lower values find fainter artefacts but also more false positives. |
| `--min-contrast` | Smallest `peak_amp / noise`: how far the artefact's peak rises above the per-pixel noise. `0` disables the test. |
| `--ghost-sigma` | Expected artefact sizes, in pixels: small, medium, large. |
| `--search-radius` | Limit the search around `--coord`. `0` searches the whole window. |

`--min-snr` grows with the number of pixels a kernel covers, so a broad, faint
background gradient can pass it while being invisible. `--min-contrast` tests
the peak surface brightness instead. It is most useful with `--dirty`: the
extracted profile includes the artefact's compact core, which a wide Gaussian
averages away. With the profile, a real ghost typically has a contrast of 3 or
more, while diffuse background structure stays below 1.

When `--dirty` is used, the extracted profile is saved as two files:

- `output/<filter>/artefact_profile.png` - a grayscale view of the matched-filter
  kernel, with each kernel sample drawn as an 8x8 block and the brightest
  sample in white;
- `output/<filter>/artefact.npe` - the numerical kernel, with the extraction
  metadata. The file is text: comment lines record the format version, dirty
  frame, centre, radius, noise and masked-pixel count, then `radius*2+1` rows
  each hold `radius*2+1` full-precision samples in row-major order. The kernel
  is normalised to sum one, exactly as used by `detect`.

The result is `output/<filter>/detection.csv`, one row per frame. The frames
listed with `detected;true` are the ones that show the artefact. The reference
frame appears with `detected;false` and empty measurement columns, because it
is the baseline. The settings used for the run, including `min_snr`, are also
written to `output/<filter>/detect.json`; later stages reuse that threshold.

If the artefact has an obvious shape, adding `--dirty` and `--dirty-coord`
usually improves the separation between frames that have it and frames that
do not. The `kernel` column of the report then shows `profile` instead of
`gaussian`.

## Step 2 - scan

`scan` reads the detection table, averages the frames that contain the artefact
into one master, averages the clean frames into another, and subtracts the
second from the first. Stars, sky background and everything else present in
both groups cancel. What remains is the artefact, plus any other artefact that
was present in some frames but not in others. The whole difference image is
then searched, so artefacts far from the coordinate you supplied in step 1 are
found too.

If step 1 was run with `--dirty`, the extracted shape is in
`output/<filter>/artefact.npe`. Add `--profile` to use that shape as an
additional matched filter:

```
./bin/exorciser scan --filter L --profile
```

The profile replaces the Gaussian `--ghost-sigma` scales by default; give
`--ghost-sigma` explicitly as well to search for both the profile and the
generic shapes. The profile's amplitude is not fixed: the signal-to-noise ratio
scales linearly with intensity, so a fainter or brighter copy is still found.
Its apparent size can change, so `--profile-scale` tries several multipliers of
the profile radius. The default is `0.75,1,1.25`; multipliers below 0.75 are
available but tend to match compact stars and other sharp residuals. Use
`--profile-file` to read a profile from somewhere other than
`<output>/<filter>/artefact.npe`.

A profile score is not on the same scale as a Gaussian score. The compact
profile kernel usually gives much larger `snr` values, and the small-scale
multipliers are sensitive to compact residuals. `--max-peak-ratio` (default 15)
rejects a candidate whose local peak sits more than that multiple above its
matched amplitude, which removes stars and sharp defects. Start with a higher
`--min-snr` than you used for the Gaussian scan and inspect `artefacts.csv`
before feeding it to `annotate` or `union`.

Examples (from `output/cmd/L_scan.sh` and `output/cmd/B_scan.sh`):

```
./bin/exorciser scan -n 10 --ghost-sigma 8 --margin 4 -j 8 \
  --min-snr 10 --polarity positive --filter L -i img/registered/ --star-halo 280
```

```
./bin/exorciser scan -n 16 --ghost-sigma 8 --margin 4 -j 8 \
  --min-snr 10 --polarity positive --filter B -i img/registered/
```

Main options:

| Option | Meaning |
| --- | --- |
| `-n`, `--count` | Maximum number of frames stacked into each master. More frames means less noise. |
| `--min-snr` | Significance at which a feature is catalogued, and the threshold that decides which clean frames are too close to the cut-off to trust (see `--clean-margin`). It does not have to equal the value used by `detect`. |
| `--clean-margin` | Skips clean frames whose peak is dangerously close to `--min-snr`, so a faint artefact does not contaminate the clean master. |
| `--polarity` | `both` reports bright and dark artefacts. `positive` and `negative` keep one sign only; the example scripts use `positive`. |
| `--ghost-sigma` | Sizes searched for. The scripts use `8` because all ghosts in this data are similar in size. Ignored when `--profile` is used, unless given explicitly. |
| `--profile` | Reuse `<output>/<filter>/artefact.npe` as a matched filter. |
| `--profile-file` | Use a specific `.npe` profile instead of the default path. |
| `--profile-scale` | Profile size multipliers, default `0.75,1,1.25`. |
| `--max-peak-ratio` | Reject profile candidates whose local peak exceeds this multiple of their matched amplitude, default 15. `0` disables. |
| `--margin` | Width in pixels of the image border excluded from the scan. |
| `--corner` | Size of the square ignored at each corner. Defaults to 80; `0` disables it. |
| `--star-halo` | Radius around a star whose halo is ignored. `0` disables halo rejection. |
| `--star-halo-small`, `--star-halo-min-area`, `--star-halo-min` | Control which stars are treated as large, and what counts as a saturated star. |

The results are:

- `output/<filter>/artefacts.csv` - the catalogue.
- `output/<filter>/<filter>_stack_clean.xisf` - the master built from clean
  frames, window name `clean_<filter>`.
- `output/<filter>/<filter>_stack_ghost.xisf` - the master built from frames
  containing the artefact, window name `ghost_<filter>`.

Stars are the strongest residual in the difference, so their cores are masked
and their wider haloes rejected. The image border and the four corner squares
are also ignored, because registration leaves them poorly defined.

## Step 3 - annotate

`annotate` turns the ghost master into a colour image and draws a red cross at
every position in the catalogue, with the catalogue number beside it.

Example (from `output/cmd/L_annotate.sh`):

```
./bin/exorciser annotate --filter L --label-scale 5 --thickness 2 --arm 12
```

| Option | Meaning |
| --- | --- |
| `--filter` | Which filter to annotate. |
| `--arm` | Length of the cross arms in pixels. |
| `--thickness` | Thickness of the cross lines. |
| `--label-scale` | Size of the numbers; `0` hides them. |

The result is `output/<filter>/<filter>_stack_detected.xisf`, which opens in
PixInsight as `detected_<filter>`. The crosses are red because the ghost image
itself is neutral grey.

## Step 4 - union

`union` runs once, after all filters have been detected, scanned and annotated.

```
./bin/exorciser union
```

It reads the catalogues of every single-letter filter directory found under
`output/` and writes them into a directory named after those filters in LRGBSHO
order. With L, R, G and B present, that is `output/LRGB/`.

The same ghost appears in every filter, but its centre moves by a few pixels
because each filter is stacked from its own frames. `union` treats positions
closer than `--tolerance` pixels (16 by default) as the same artefact and
averages them back to one rounded coordinate. SNR, amplitude and size are
averaged as well.

It writes:

- `output/LRGB/artefacts.csv` - the merged catalogue across filters.
- `output/LRGB/background.csv` - the centre and statistics of a background sky
  window (see below).
- `output/LRGB/<filter>_stack_detected.xisf` - a copy of each filter's
  annotated image with **yellow** crosses added.

A yellow cross and its number mark a union artefact that this filter did not
detect but another filter did. Red crosses are the artefacts the filter found
itself. The yellow numbers are the numbers from `output/LRGB/artefacts.csv`.

### The background window

`union` also chooses a patch of empty sky to represent the background. It uses
the clean master (`<filter>_stack_clean.xisf`) of the first available filter in
the order **L, R, H**, because a luminance frame usually has the best
signal-to-noise.

The patch is a 65x65 square. 65 is odd on purpose: the window then has a true
centre pixel, at `x-32 ... x+32`. The whole square lies inside the image, and
it must stay clear of every artefact in the union catalogue by three times that
artefact's size, so a ghost's edge cannot leak into the sample.

Stars inside the window are removed before anything is measured. A star mask is
built from the clean master, then the remaining pixels are sigma-clipped to
remove faint stars and cosmic rays the mask missed. A window is only accepted
when at least 75% of its pixels survive and each of its four corner quadrants
keeps at least 25%. A background sample may contain a few stars, but most of
the window must still contribute to the statistics.

Uniformity decides the winner. The window is divided into four 32x32 corner
quadrants, and the spread between their average levels is measured in units of
the frame's noise. The flattest patch wins. Its average level only acts as a
tie-breaker, with a small weight, so a uniform patch is preferred to a brighter
or darker one. This is what keeps the sample out of nebulosity: a nebula raises
the quadrant spread even when its average brightness happens to equal the
frame's median, which is exactly how the old level-only rule went wrong.

The centre, the statistics of the surviving pixels and their count are written
to `output/LRGB/background.csv`:

```
filter;x;y;size;mean;noise;snr;pixels
L;3456;1056;65;0,009465610751035629;0,00011199150197167373;84,521;3769
```

`x;y` is the centre in registered pixels, `mean` the sky level, `noise` its
standard deviation, `snr` their ratio (`mean / noise`), and `pixels` how many
samples survived the mask and clipping out of the 4225 in the window.

The same square is outlined in yellow on every `<filter>_stack_detected.xisf`
written by `union`, so you can see exactly which part of the image supplied the
background value.

Options:

| Option | Meaning |
| --- | --- |
| `-o`, `--output` | Root directory holding the filter directories. Defaults to `output`. |
| `--tolerance` | Maximum distance in pixels for two positions to be considered the same artefact. Raise it if a filter's copy appears in yellow by mistake. |
| `--arm`, `--thickness`, `--label-scale` | Same meaning as in `annotate`, for the yellow markers. |

## Step 5 - erase

`erase` removes the catalogued artefacts from the frames themselves. It reads
the union catalogue, so every artefact found by any filter is removed, not
only the ones the current filter detected itself.

```
./bin/exorciser erase --filter L
```

A frame is only modified when `output/<filter>/detection.csv` marks it
`detected;true`. Clean frames are copied unchanged, so the output directory
always contains every input frame with its original file name. Frames missing
from the report are also copied unchanged, with a warning.

With `--inplace` the detected frames are rewritten in the input directory
instead. No copies are made, and clean or unlisted frames are left exactly as
they are. The originals are replaced, so there is no backup: copy the frames
first if you need to keep them.

The repair uses files written earlier:

- the union catalogue, default `output/LRGB/artefacts.csv`;
- the detection settings, default `output/<filter>/detect.json`;
- the matched-filter profile, default `output/<filter>/artefact.npe`;
- the background window, default `output/LRGB/background.csv`;
- the reference master, default `output/L/L_stack_clean.xisf` (that is,
  `output/<filter>/<filter>_stack_clean.xisf`).

The catalogue position is only a starting point. For every artefact in a
detected frame, `erase` searches a circular `--search-radius` around it using
the saved profile, or a Gaussian at the catalogue `ghost_sigma` when no profile
is available. Peaks within `--search-guard` pixels of that radius are rejected,
so a bright object just outside the expected region is not mistaken for the
artefact. The frame is fitted to the clean master once, and every local window
gets its own background plane and star mask. If the local peak is below the
threshold recorded in `detect.json`, that artefact is skipped. Use
`--min-snr` to override the stored threshold.

The repaired disc starts at `--radius` and grows with `ghost_sigma` according
to `--radius-scale`. With the defaults, a `ghost_sigma` of 10 gives a 13 px
disc plus the 4 px feather. The frame's local sky is modelled with a first-order
plane, the clean structure is scaled by the frame-to-clean affine slope, and
Gaussian noise is added to match the local noise. The local annulus excludes
star-mask pixels; if it is much noisier than the global background, the global
`background.csv` window is used instead. A `--feather` pixel blend hides the
rim. The noise is seeded from the frame name and
artefact number, so repeated runs give the same result.

Every decision is written to `output/<filter>/inpaint.csv`:

```
frame;artefact;catalogue_x;catalogue_y;found_x;found_y;snr;radius;applied;calibration;reason
```

`calibration` is `local` when the annulus around the found position was used,
`global` when the `background.csv` window was the fallback, and empty for a
skipped artefact. `reason` explains the skip.

By default the repaired frames are written to `output/registered/<filter>/`
and the input frames are never modified. With `--inplace` the detected frames
are replaced in place and nothing is written to the output directory.

**Warning: passing clouds break the repair.** Inpainting assumes the sky
background is stable between the frame and the clean master. A passing cloud
changes the background level and, more importantly, puts a gradient across the
frame. The 65x65 background window no longer represents the sky under the
artefact, so the clean patch is placed at the wrong local level and a faint
circular mark remains. This is a limitation of the method, not of the
catalogue: `detected;true` only reports the supervised artefact, so nothing in
the pipeline screens frames for cloud. Inspect repaired frames before using
them, and exclude cloudy frames from the erase input.

Main options:

| Option | Meaning |
| --- | --- |
| `-i` | Directory holding the frames, default `img/registered`. |
| `-o` | Output root, default `output/registered`. |
| `--inplace` | Rewrite detected frames in the input directory instead of copying them. |
| `--filter` | Filter to repair. |
| `--products` | Directory holding the scan, annotate and union results, default `output`. |
| `--artefacts` | Union catalogue to use, when it is not the default. |
| `--background` | Background window record, when it is not the default. |
| `--clean` | Clean master to use as reference. |
| `--detection` | Detection report deciding which frames are modified. |
| `--radius` | Minimum radius of the repaired disc, default 12 pixels. |
| `--radius-scale` | Per-artefact radius multiplier of `ghost_sigma`, default 1.25; `0` keeps `--radius` fixed. |
| `--feather` | Blend width inside the rim, default 4 pixels. |
| `--search-radius` | Circular radius searched around each catalogue position, default 24 pixels. |
| `--search-guard` | Peaks within this distance of the search radius are rejected, default 4 pixels. |
| `--min-snr` | Local detection threshold; default comes from `detect.json`. |
| `-j` | Number of frames processed at once, default 4. |

## Running every filter

`output/cmd/all.sh` clears the old results and runs the three per-filter scripts
in order:

```
output/cmd/L_detect.sh
output/cmd/L_scan.sh
output/cmd/L_annotate.sh
output/cmd/R_detect.sh
...
```

Then run:

```
./bin/exorciser union
```

Finally, repair each filter:

```
./bin/exorciser erase --filter L
./bin/exorciser erase --filter R
./bin/exorciser erase --filter G
./bin/exorciser erase --filter B
```

Repaired frames land in `output/registered/<filter>/`. Add `--inplace` to
rewrite the registered frames themselves instead, for example
`./bin/exorciser erase --filter L --inplace`.

`output/cmd/L.sh`, `R.sh`, `G.sh` and `B.sh` run one filter each. The scripts
assume you are in the project directory and that the program is at
`./bin/exorciser`.

**Warning:** `all.sh` starts with `rm -rf output/L output/R output/G output/B`.
Annotated images you modified in PixInsight, or catalogues you still need, are
deleted by that line. Copy them elsewhere first, or comment the line out.

Each filter has its own coordinate, reference frame and dirty frame, because
the artefacts are only approximately at the same place and the best clean
reference differs between filters. The coordinate for L will usually be within
a few pixels of the one for B, but measure it in each filter.

## Reading the results

### Files

| File | Contents |
| --- | --- |
| `output/<filter>/detection.csv` | Per-frame detection table from `detect`. |
| `output/<filter>/detect.json` | Settings used by `detect`, including the localisation threshold. |
| `output/<filter>/artefact_profile.png` | Visual profile kernel from `detect --dirty`. |
| `output/<filter>/artefact.npe` | Numerical profile kernel from `detect --dirty`. |
| `output/<filter>/artefacts.csv` | Artefact catalogue from `scan`. |
| `output/<filter>/<filter>_stack_clean.xisf` | Stack of the clean frames. |
| `output/<filter>/<filter>_stack_ghost.xisf` | Stack of the frames containing the artefact. |
| `output/<filter>/<filter>_stack_detected.xisf` | Colour ghost stack with red annotations. |
| `output/<LRGBSHO...>/artefacts.csv` | Merged catalogue from `union`. |
| `output/<LRGBSHO...>/background.csv` | Centre and sky statistics of the background window. |
| `output/<LRGBSHO...>/<filter>_stack_detected.xisf` | Annotated image with yellow markers for missing artefacts and the background square. |
| `output/<filter>/inpaint.csv` | Per-artefact localisation and repair report from `erase`. |
| `output/registered/<filter>/<name>.xisf` | Repaired frames written by `erase`; clean frames are unchanged copies. |

Both CSV files use a semicolon as separator and a comma as decimal separator,
so they open directly in a spreadsheet with the right numbers.

### detection.csv

The columns that matter most:

| Column | Meaning |
| --- | --- |
| `file` | Frame file name. |
| `detected` | `true` when the artefact was found in that frame. |
| `peak_snr` | Strength of the detection. Higher is stronger; the `--min-snr` value is the cut-off. |
| `peak_amp` | Peak amplitude in pixel units. `peak_amp / noise` is the contrast tested by `--min-contrast`. |
| `peak_x`, `peak_y` | Where the strongest response was found in that frame. |
| `ghost_sigma` | Apparent size of the feature in pixels. |
| `error` | Why a frame could not be measured, if that happened. |

The other columns (`norm_a`, `norm_b`, `noise`, `clipped_px`, `duration_ms`)
are diagnostics used when a run has to be investigated.

### artefacts.csv

| Column | Meaning |
| --- | --- |
| `n` | Rank, strongest first. |
| `x`, `y` | Position in registered pixels, counted from 0 at the top-left. |
| `snr` | Significance. Positive means brighter than the clean master, negative means darker. |
| `amp` | Smoothed amplitude in pixel units; usually a small number such as `0.0001`. |
| `ghost_sigma` | Apparent size of the artefact in pixels. |

A catalogue entry is a candidate, not a certainty. Check each strong entry on
the annotated image and, if needed, on the raw frames.

### Annotated images

Red means "found in this filter". A yellow cross, in the union directory only,
means "found in another filter but not in this one". A yellow marker may also
mean that the artefact was present but too faint to pass `--min-snr`, or that
it fell inside the excluded border or a star halo. A hollow yellow square is
the background sky window, not an artefact: it marks the region whose sky level
and noise are recorded in `background.csv`.

## Options worth changing

### detect

| Option | Default | Change it when |
| --- | --- | --- |
| `--window` | 128 | The artefact is larger than about 100 pixels. |
| `--min-snr` | 6 | Too many or too few frames are marked detected. |
| `--min-contrast` | 0 | Diffuse background structure passes `--min-snr`. Use about 1.5 with `--dirty`. |
| `--ghost-sigma` | 4,8,16 | Every artefact of interest is larger or smaller than these sizes. |
| `--search-radius` | 0 | A bright object near the artefact confuses the search. |
| `--dirty`, `--dirty-coord` | unset | The artefact has a distinctive shape and detection is ambiguous. |

### scan

| Option | Default | Change it when |
| --- | --- | --- |
| `-n`, `--count` | 10 | The masters look noisy, or there are fewer frames available. |
| `--min-snr` | 6 | You want fainter artefacts (lower) or fewer candidates (higher). |
| `--clean-margin` | 0.2 | Scan refuses to find two clean frames. |
| `--polarity` | both | You only care about bright (positive) or dark (negative) ghosts. |
| `--ghost-sigma` | 4,8,16 | Artefacts are consistently larger or smaller. |
| `--profile` | off | You want to catalogue the shape measured by `detect --dirty`. |
| `--profile-scale` | 0.75,1,1.25 | The same shape appears at clearly different sizes. |
| `--max-peak-ratio` | 15 | Lower it to reject sharper candidates; raise it to keep more. |
| `--margin` | 64 | A real artefact sits near the edge of the frame. |
| `--corner` | 80 | A real artefact sits in a corner, or corner false positives remain. |
| `--star-halo` | 220 | Star haloes are still catalogued, or a real artefact near a bright star disappears. |

### annotate and union

| Option | Default | Change it when |
| --- | --- | --- |
| `--arm` | 12 | Crosses are too big or too small on the image. |
| `--thickness` | 3 | Crosses are too heavy or too thin. |
| `--label-scale` | 3 | Numbers are hard to read. |
| `--tolerance` (union) | 16 | The same artefact is split in two, or two different artefacts are merged. |

## Troubleshooting

**"cannot determine filter from the input; pass --filter"**
The frame directory name does not contain `_FILTER-L_` or similar. Pass
`--filter L` (or R, G, B, S, H, O) explicitly.

**"frames named in the report were not found"**
`scan` reads the names from the report produced by `detect`, and those files
must be present in the directory you pass to `scan`. Use the same input for
both, or copy the report to the location that matches.

**"no artefact profile at …; run detect --dirty or pass --profile-file"**
`scan --profile` looks for `output/<filter>/artefact.npe`. Run
`detect --dirty` first, or give the profile location with `--profile-file`.
If you do not want profile matching, omit `--profile`.

**"detection report has N usable clean frames, need at least 2"**
Too few frames were classified as clean. Lower `--min-snr` in `scan`, raise
`--clean-margin`, or add frames from another night. The same message about
detected frames means too few frames show the artefact strongly enough.

**Nothing is found**
Check that the coordinate is in the registered frames and that the window
actually contains the artefact. Increase `--window`, lower `--min-snr`, and
make sure the polarity is not set to the wrong sign. Verify that the reference
frame really is clean.

**The artefact disappears completely**
The reference frame probably contains it as well, so it cancels in the
subtraction. Choose another reference.

**A frame passes `--min-snr` but no artefact is visible**
The field's own diffuse background varies slightly between frames, and a broad
kernel integrates that structure into a significant peak. Add `--dirty` with a
frame where the artefact is obvious, so the core of the artefact is matched,
then set `--min-contrast` to about `1.5`. A real ghost's core rises several
noise sigmas above the background; diffuse structure stays near or below one.

**Yellow markers are in the wrong place**
Positions differ by more than the tolerance between filters. Raise
`--tolerance` in `union`.

**"none of the filters L, R, H has a clean master"**
`union` needs `L_stack_clean.xisf`, `R_stack_clean.xisf` or
`H_stack_clean.xisf` to choose the background window. Run `scan` for one of
those filters first.

**"no window of sky is free of artefacts with enough surviving pixels"**
No 65x65 patch is both clear of the catalogue and mostly star-free. Review the
catalogue for false positives, or use a filter with fewer artefacts and fewer
stars as the background source.

**"union needs at least two filter catalogues ... found 1"**
`erase` derives the union directory from the filter folders under `--products`.
Run `scan` for at least two filters and then `union`, or pass `--artefacts`
explicitly.

**`erase` copied a frame instead of repairing it**
The frame is not marked `detected;true` in `output/<filter>/detection.csv`, or
it is missing from that report. Both are reported in the log.

**A round mark appears where an artefact was repaired**
The frame has passing cloud or a steep background gradient, so the clean patch
sits at the wrong local level. Exclude the frame and run `erase` again.

**An artefact is listed in inpaint.csv but applied is false**
Check the `reason` column. `below min-snr` means the local peak was too weak for
the threshold stored in `detect.json`; lower `--min-snr` to accept it. A
window or fit error means the artefact is too close to the frame edge for the
available background ring.

**A known artefact is missing from one filter**
It may be below that filter's threshold, or inside the excluded border or a
star halo. Try a lower `--min-snr` and a smaller `--star-halo` for that filter,
then rerun `scan`, `annotate` and `union`.

## How it works

`detect` treats the difference between two linear frames as a simple
brightness relation, frame = a x reference + b. It fits that relation on the
whole image using medians, so stars and small misalignments do not distort it.
After removing the relation and the local background, it looks for broad,
smooth patches in a square window. A ghost spreads its light over tens of
pixels and is invisible in any single pixel, but adding the neighbouring pixels
together lifts it above the noise. That combined significance is `peak_snr`.

`scan` builds two masters with sigma-clipped averaging, one from frames with
the artefact and one from clean frames, and subtracts them. Anything common to
both groups cancels. Star cores are masked, star haloes are rejected, and the
outer border and corners are excluded. The remaining difference is searched at
several sizes, and each significant peak is reported once, strongest first.

`annotate` converts the ghost master to RGB and draws the catalogue. `union`
matches catalogues across filters by position, averages the slightly different
coordinates, and adds the yellow markers. `erase` copies the corresponding
ghost-free patch from the clean master into each frame, shifts it to the
frame's sky level and adds matched noise, so the repaired area has the same
brightness and grain as its surroundings.

## Known limitations

- Frames must be star-aligned and linear. Stretched or unregistered frames give
  meaningless measurements.
- An artefact present in the reference frame cancels and cannot be found.
- Only diffuse artefacts are targeted. Stars, cosmic rays and hot pixels are
  masked or rejected on purpose.
- Positions are in registered pixels. If you restack or re-register the
  frames, previously measured coordinates move.
- One filter is processed per run. `union` combines the results afterwards.
- Inpainting cannot compensate for passing cloud or a strong sky gradient. The
  repair leaves a visible round mark on such frames.
