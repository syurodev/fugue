package secrets

// Only the longest terminal suffix is needed: every shorter suffix at the same
// end is contained in it. This avoids quadratic output for nested known values.
type node struct {
	next   [256]int
	fail   int
	length int
	kind   string
}

func (d *Detector) insert(value, kind string) {
	state := 0
	for i := range len(value) {
		b := value[i]
		next := d.nodes[state].next[b]
		if next == 0 {
			next = len(d.nodes)
			d.nodes = append(d.nodes, node{})
			d.nodes[state].next[b] = next
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
	for b := range 256 {
		if next := d.nodes[0].next[b]; next != 0 {
			queue = append(queue, next)
		}
	}
	for i := 0; i < len(queue); i++ {
		state := queue[i]
		fail := d.nodes[state].fail
		for b := range 256 {
			next := d.nodes[state].next[b]
			fallback := d.nodes[fail].next[b]
			if next == 0 {
				d.nodes[state].next[b] = fallback
				continue
			}
			d.nodes[next].fail = fallback
			if d.nodes[fallback].length > d.nodes[next].length {
				d.nodes[next].length = d.nodes[fallback].length
				d.nodes[next].kind = d.nodes[fallback].kind
			}
			queue = append(queue, next)
		}
	}
}

func (d *Detector) findKnown(text string) []Finding {
	if len(d.nodes) <= 1 {
		return nil
	}
	var findings []Finding
	state := 0
	for i := range len(text) {
		state = d.nodes[state].next[text[i]]
		n := &d.nodes[state]
		if n.length > 0 {
			findings = append(findings, Finding{Start: i + 1 - n.length, End: i + 1, Kind: n.kind})
		}
	}
	return findings
}
