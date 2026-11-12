package wechat

import (
	"errors"
	"fmt"
	"strings"
)

// QRCodeSVG encodes byte-mode QR version 10, error correction L, mask 0.
// Its 271-byte capacity covers the official binding URL (currently 89 bytes).
// A fixed, bounded symbol avoids a new production dependency or a third-party
// QR service receiving account-linking URLs.
func QRCodeSVG(text string) (string, error) {
	if text == "" || len(text) > 271 {
		return "", errors.New("wechat_qr_invalid")
	}
	bits := make([]bool, 0, 2192)
	add := func(value, count int) {
		for i := count - 1; i >= 0; i-- {
			bits = append(bits, value>>i&1 != 0)
		}
	}
	add(4, 4)
	add(len(text), 16)
	for _, b := range []byte(text) {
		add(int(b), 8)
	}
	add(0, min(4, 2192-len(bits)))
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	data := make([]byte, 0, 274)
	for i := 0; i < len(bits); i += 8 {
		var value byte
		for j := 0; j < 8; j++ {
			value <<= 1
			if bits[i+j] {
				value |= 1
			}
		}
		data = append(data, value)
	}
	for len(data) < 274 {
		data = append(data, 0xec)
		if len(data) < 274 {
			data = append(data, 0x11)
		}
	}
	multiply := func(x, y byte) byte {
		var result int
		a, b := int(x), int(y)
		for b > 0 {
			if b&1 != 0 {
				result ^= a
			}
			b >>= 1
			a <<= 1
			if a&0x100 != 0 {
				a ^= 0x11d
			}
		}
		return byte(result)
	}
	divisor := make([]byte, 18)
	divisor[17] = 1
	root := byte(1)
	for i := 0; i < 18; i++ {
		for j := range divisor {
			divisor[j] = multiply(divisor[j], root)
			if j+1 < len(divisor) {
				divisor[j] ^= divisor[j+1]
			}
		}
		root = multiply(root, 2)
	}
	blocks := make([][]byte, 4)
	checks := make([][]byte, 4)
	offset := 0
	for i := range blocks {
		count := 68
		if i >= 2 {
			count = 69
		}
		blocks[i] = data[offset : offset+count]
		offset += count
		rem := make([]byte, 18)
		for _, b := range blocks[i] {
			factor := b ^ rem[0]
			copy(rem, rem[1:])
			rem[17] = 0
			for j := range rem {
				rem[j] ^= multiply(divisor[j], factor)
			}
		}
		checks[i] = rem
	}
	encoded := make([]byte, 0, 346)
	for j := 0; j < 69; j++ {
		for _, block := range blocks {
			if j < len(block) {
				encoded = append(encoded, block[j])
			}
		}
	}
	for j := 0; j < 18; j++ {
		for _, check := range checks {
			encoded = append(encoded, check[j])
		}
	}
	const size = 57
	var modules, function [size][size]bool
	set := func(x, y int, dark bool) {
		if x >= 0 && x < size && y >= 0 && y < size {
			modules[y][x] = dark
			function[y][x] = true
		}
	}
	for i := 0; i < size; i++ {
		set(6, i, i%2 == 0)
		set(i, 6, i%2 == 0)
	}
	abs := func(n int) int {
		if n < 0 {
			return -n
		}
		return n
	}
	for _, center := range [][2]int{{3, 3}, {size - 4, 3}, {3, size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				d := max(abs(dx), abs(dy))
				set(center[0]+dx, center[1]+dy, d != 2 && d != 4)
			}
		}
	}
	positions := []int{6, 28, 50}
	for i, x := range positions {
		for j, y := range positions {
			if (i == 0 && j == 0) || (i == 0 && j == 2) || (i == 2 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					set(x+dx, y+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	// BCH-protected format information: error correction L and mask 0.
	format := 8 << 10
	for i := 14; i >= 10; i-- {
		if format>>i&1 != 0 {
			format ^= 0x537 << (i - 10)
		}
	}
	format = ((8 << 10) | format) ^ 0x5412
	bit := func(i int) bool { return format>>i&1 != 0 }
	for i := 0; i <= 5; i++ {
		set(8, i, bit(i))
	}
	set(8, 7, bit(6))
	set(8, 8, bit(7))
	set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		set(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		set(size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		set(8, size-15+i, bit(i))
	}
	set(8, size-8, true)
	version := 10 << 12
	for i := 17; i >= 12; i-- {
		if version>>i&1 != 0 {
			version ^= 0x1f25 << (i - 12)
		}
	}
	version |= 10 << 12
	for i := 0; i < 18; i++ {
		dark := version>>i&1 != 0
		x, y := size-11+i%3, i/3
		set(x, y, dark)
		set(y, x, dark)
	}
	index := 0
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vertical := 0; vertical < size; vertical++ {
			y := vertical
			if (right+1)&2 == 0 {
				y = size - 1 - vertical
			}
			for j := 0; j < 2; j++ {
				x := right - j
				if function[y][x] {
					continue
				}
				dark := index < len(encoded)*8 && encoded[index/8]>>(7-index%8)&1 != 0
				index++
				if (x+y)%2 == 0 {
					dark = !dark
				}
				modules[y][x] = dark
			}
		}
	}
	var path strings.Builder
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if modules[y][x] {
				if _, err := fmt.Fprintf(&path, "M%d %dh1v1h-1z", x+4, y+4); err != nil {
					return "", err
				}
			}
		}
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 65 65" shape-rendering="crispEdges"><path fill="#fff" d="M0 0h65v65H0z"/><path fill="#000" d="` + path.String() + `"/></svg>`, nil
}
