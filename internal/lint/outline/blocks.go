package outline

import (
	"bytes"
	"strconv"
	"strings"

	ts "github.com/odvcencio/gotreesitter"
)

// Block is one outermost named function or method, or one TypeScript test
// call, as semantic similarity compares it.
type Block struct {
	// Symbol is unique in its file: Name, Receiver.Name, or test "title",
	// with #2, #3, ... on repeats.
	Symbol string
	Pos    Pos
	// Content runs from the start of the line that names the block to its
	// end, without comments, docstrings, a leading export, and the first
	// line's indentation, so a method and a function compare alike.
	Content string
}

type pendingBlock struct {
	symbol string
	anchor *ts.Node
	node   *ts.Node
}

// block queues node for finishBlocks unless it lies in another block: an
// inner function is part of its outer block. The walk visits outer nodes
// first.
func (w *walker) block(symbol string, anchor, node *ts.Node) {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if w.blockSpans[[2]uint32{parent.StartByte(), parent.EndByte()}] {
			return
		}
	}

	if w.blockSpans == nil {
		w.blockSpans = make(map[[2]uint32]bool)
	}

	w.blockSpans[[2]uint32{node.StartByte(), node.EndByte()}] = true
	w.blocks = append(w.blocks, pendingBlock{symbol: symbol, anchor: anchor, node: node})
}

func (w *walker) finishBlocks() {
	if len(w.blocks) == 0 {
		return
	}

	seen := make(map[string]int)
	comment := make([]bool, len(w.src))

	for _, each := range w.comments {
		for offset := each.start; offset < each.end; offset++ {
			comment[offset] = true
		}
	}

	for _, pending := range w.blocks {
		symbol := pending.symbol

		seen[symbol]++
		if count := seen[symbol]; count > 1 {
			symbol += "#" + strconv.Itoa(count)
		}

		start := min(pending.anchor.StartByte(), pending.node.StartByte())
		start = uint32(bytes.LastIndexByte(w.src[:start], '\n') + 1)

		// An export changes no behavior, so a copy that is exported and one
		// that is not compare alike.
		content := blockContent(w.src[:pending.node.EndByte()], start, comment)
		content = strings.TrimPrefix(strings.TrimPrefix(content, "export "), "default ")

		w.file.Blocks = append(w.file.Blocks, Block{
			Symbol:  symbol,
			Pos:     w.pos(pending.anchor),
			Content: content,
		})
	}
}

// blockContent returns src[start:] without the bytes comment marks. A
// line that held only comment text is dropped.
func blockContent(src []byte, start uint32, comment []bool) string {
	var (
		lines  []string
		prefix string
	)

	for lineStart := int(start); lineStart < len(src); {
		lineEnd := len(src)
		if newline := bytes.IndexByte(src[lineStart:], '\n'); newline >= 0 {
			lineEnd = lineStart + newline
		}

		var line strings.Builder

		for offset := lineStart; offset < lineEnd; offset++ {
			if !comment[offset] {
				line.WriteByte(src[offset])
			}
		}

		text := strings.TrimRight(line.String(), " \t")
		if len(lines) == 0 {
			prefix = text[:len(text)-len(strings.TrimLeft(text, " \t"))]
		}

		if text != "" || len(bytes.TrimSpace(src[lineStart:lineEnd])) == 0 {
			lines = append(lines, strings.TrimPrefix(text, prefix))
		}

		lineStart = lineEnd + 1
	}

	return strings.Join(lines, "\n")
}
