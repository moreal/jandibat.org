#!/bin/sh

# Read at most 64 bytes. Emit one lexical stage identifier or nothing; callers
# retain their explicit allowlists and fixed failure annotations.
read_image_validation_stage() {
	dd if="${1:-}" bs=64 count=1 2>/dev/null | od -An -tu1 | awk '
		{ for (i = 1; i <= NF; i++) {
			count++; byte = $i
			if (byte == 10) { if (newline++) invalid = 1 }
			else if (newline || (byte != 45 && (byte < 97 || byte > 122))) invalid = 1
			else value = value sprintf("%c", byte)
		} }
		END { if (!invalid && count > 0 && count < 64 && length(value) > 0) print value }
	'
}
