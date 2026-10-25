// Package union merges the artefact catalogues of several filters into one
// list, and reports which entries are missing from a given filter.
//
// A ghost is visible in every filter, but its position differs slightly
// between them because each filter's master is stacked from its own registered
// frames. The same artefact therefore appears in each catalogue with a
// jittered coordinate. Merge clusters positions within a tolerance and
// averages them back to one entry.
package union

import (
	"math"
	"sort"

	"github.com/hoxca/exorciser/internal/report"
)

// Filter is one filter's catalogue.
type Filter struct {
	Name      string
	Artefacts []report.Artefact
}

// entry is one catalogue row, tagged with the filter it came from.
type entry struct {
	filter int
	art    report.Artefact
}

// cluster is a group of entries that describe the same artefact.
type cluster struct {
	members []entry
	seen    map[int]bool
	sumX    float64
	sumY    float64
	sumSNR  float64
	sumAmp  float64
	sumSize float64
}

func (c *cluster) add(e entry) {
	c.members = append(c.members, e)
	c.seen[e.filter] = true
	c.sumX += float64(e.art.X)
	c.sumY += float64(e.art.Y)
	c.sumSNR += e.art.SNR
	c.sumAmp += e.art.Amp
	c.sumSize += e.art.Scale
}

func (c *cluster) count() int { return len(c.members) }

func (c *cluster) x() float64 { return c.sumX / float64(c.count()) }
func (c *cluster) y() float64 { return c.sumY / float64(c.count()) }

// Merge clusters the entries of every filter and returns one artefact per
// cluster, ordered by decreasing mean |SNR|.
//
// An entry joins the nearest cluster inside tolerance pixels, but a cluster
// accepts at most one entry per filter: two catalogue rows of one filter are
// already distinct artefacts, so they must not be averaged together. Entries
// are visited strongest first, so the strongest observation of an artefact
// seeds its cluster. The merged position is the rounded mean of the member
// positions; SNR, amp and ghost scale are averaged too.
func Merge(filters []Filter, tolerance float64) []report.Artefact {
	var entries []entry
	for fi, f := range filters {
		for _, a := range f.Artefacts {
			entries = append(entries, entry{filter: fi, art: a})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entryLess(entries[i], entries[j])
	})

	toleranceSq := tolerance * tolerance
	var clusters []*cluster
	for _, e := range entries {
		var best *cluster
		bestDist := math.Inf(1)
		for _, c := range clusters {
			if c.seen[e.filter] {
				continue
			}
			dx := c.x() - float64(e.art.X)
			dy := c.y() - float64(e.art.Y)
			d := dx*dx + dy*dy
			if d <= toleranceSq && d < bestDist {
				best = c
				bestDist = d
			}
		}
		if best == nil {
			c := &cluster{seen: make(map[int]bool)}
			c.add(e)
			clusters = append(clusters, c)
			continue
		}
		best.add(e)
	}

	merged := make([]report.Artefact, len(clusters))
	for i, c := range clusters {
		n := float64(c.count())
		merged[i] = report.Artefact{
			X:     int(math.Round(c.sumX / n)),
			Y:     int(math.Round(c.sumY / n)),
			SNR:   c.sumSNR / n,
			Amp:   c.sumAmp / n,
			Scale: c.sumSize / n,
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		return artefactLess(merged[i], merged[j])
	})
	for i := range merged {
		merged[i].N = i + 1
	}
	return merged
}

// entryLess orders entries by decreasing |SNR|, then by filter, y and x so the
// clustering does not depend on map iteration or catalogue order.
func entryLess(a, b entry) bool {
	aa, bb := math.Abs(a.art.SNR), math.Abs(b.art.SNR)
	switch {
	case aa != bb:
		return aa > bb
	case a.filter != b.filter:
		return a.filter < b.filter
	case a.art.Y != b.art.Y:
		return a.art.Y < b.art.Y
	default:
		return a.art.X < b.art.X
	}
}

// artefactLess orders merged artefacts by decreasing |SNR|, then y and x.
func artefactLess(a, b report.Artefact) bool {
	aa, bb := math.Abs(a.SNR), math.Abs(b.SNR)
	switch {
	case aa != bb:
		return aa > bb
	case a.Y != b.Y:
		return a.Y < b.Y
	default:
		return a.X < b.X
	}
}

// Missing returns the union entries that have no counterpart in filter, that
// is, no catalogue row within tolerance pixels of the merged position.
func Missing(merged []report.Artefact, filter Filter, tolerance float64) []report.Artefact {
	toleranceSq := tolerance * tolerance
	var missing []report.Artefact
	for _, u := range merged {
		found := false
		for _, a := range filter.Artefacts {
			dx := float64(a.X - u.X)
			dy := float64(a.Y - u.Y)
			if dx*dx+dy*dy <= toleranceSq {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, u)
		}
	}
	return missing
}
