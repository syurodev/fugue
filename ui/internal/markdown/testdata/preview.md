# Markdown preview

This file gathers the DA-E03 cases the renderer is checked against. Open it with `go run ./cmd/mdpreview < internal/markdown/testdata/preview.md`.

## Whitespace around styled text

Words with **bold** and `inline code` keep the spaces the source has: a **bold** word, `code`, then**bold glued**to text and `a  double  space` inside code.

Emphasis *italic*, ***both***, ~~strikethrough~~ and a hard break here  
continue on the next line.

## Lists

- first item
- second item with `code` and **bold**
  - nested item one
  - nested item two
    1. deeply nested ordered
    2. and its sibling
- third item

3. starts at three
4. and goes on

Tasks:

- [x] a finished task
- [ ] an open task with a [link](https://example.com/tasks)

## Tables

| Left | Center | Right | None |
| :--- | :---: | ---: | --- |
| a | b | c | d |
| longer cell | `code` | **12** | text that is long enough to need a wrap in a narrow window |

## Code

A Go block with a tab-indented body:

```go
package main

import "fmt"

// greet says hello.
func greet(name string) string {
	msg := "hello, " + name // a comment
	return msg
}

func main() {
	fmt.Println(greet("fugue"), 42)
}
```

A TypeScript block:

```ts
interface Item { price: number; label: string }

// total adds the prices.
export function total(items: Item[]): number {
	return items.reduce((acc, item) => acc + item.price, 0);
}
```

A block in an unknown language stays plain, with a very long line that scrolls sideways instead of wrapping inside a token:

```nolang
plain text, no colors: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa end
	tabbed line
```

    indented code block

## Links

Hover or Tab to a link to see the popover: [the docs](https://example.com/docs/guide?a=1&b=2), a [long URL](https://example.com/a/very/long/path/that/keeps/going/and/going/and/going/until/the/popover/has/to/cut/the/middle/of/it/file.html?query=value#fragment), an [IDN host](https://bücher.example/katalog) and its [punycode form](https://xn--bcher-kva.example/katalog), and a [mail link](mailto:someone@example.org).

Blocked schemes stay plain text with no popover: [script](javascript:alert(1)), [file](file:///etc/passwd), [data](data:text/html,hi) and a [relative path](./local/page).

## Quote and rule

> A quoted paragraph with **bold** text.
>
> - and a quoted list

---

## Raw HTML and images

<div onclick="alert(1)">raw html is literal text</div>

Inline <b>tags</b> are literal too, and an image shows its alt text only, never loading: ![diagram of the host](https://example.com/diagram.png)

## Long unbreakable word

Supercalifragilisticexpialidocious_and_then_some_more_characters_without_any_space_to_wrap_at_all_in_a_narrow_window_the_paragraph_clips_it.
