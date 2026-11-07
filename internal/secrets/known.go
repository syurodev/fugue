package secrets

// Only the longest terminal suffix is needed: every shorter suffix at the same
// end is contained in it. This avoids quadratic output for nested known values.
type node struct {
	edges  map[byte]int
	fail   int
	length int
	kind   string
}

func (d *Detector) insert(value, kind string) {
	state := 0
	for i := range len(value) {
		next, ok := d.nodes[state].edges[value[i]]
		if !ok {
			next = len(d.nodes)
			d.nodes = append(d.nodes, node{edges: make(map[byte]int)})
			d.nodes[state].edges[value[i]] = next
		}
		state = next
	}
	n := &d.nodes[state]
	// Duplicate values use a stable source order independent of caller/map order.
	if n.length == 0 || kind < n.kind {
		n.length, n.kind = len(value), kind
	}
}

func (d *Detector) buildFailures() {
	queue := make([]int, 0, len(d.nodes))
	for _, next := range d.nodes[0].edges {
		queue = append(queue, next)
	}
	for i := 0; i < len(queue); i++ {
		state := queue[i]
		for b, next := range d.nodes[state].edges {
			fallback := d.advance(d.nodes[state].fail, b)
			d.nodes[next].fail = fallback
			if d.nodes[fallback].length > d.nodes[next].length {
				d.nodes[next].length = d.nodes[fallback].length
				d.nodes[next].kind = d.nodes[fallback].kind
			}
			queue = append(queue, next)
		}
	}
}

func (d *Detector) advance(state int, b byte) int {
	for {
		if next, ok := d.nodes[state].edges[b]; ok {
			return next
		}
		if state == 0 {
			return 0
		}
		state = d.nodes[state].fail
	}
}

func (d *Detector) findKnown(text string) []Finding {
	if len(d.nodes) == 0 {
		return nil
	}
	var findings []Finding
	state := 0
	for i := range len(text) {
		state = d.advance(state, text[i])
		n := d.nodes[state]
		if n.length > 0 {
			findings = append(findings, Finding{Start: i + 1 - n.length, End: i + 1, Kind: n.kind})
		}
	}
	return findings
}
