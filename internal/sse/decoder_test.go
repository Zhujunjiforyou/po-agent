package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// chunkReader 故意让每次 Read 最多返回非常少的字节。
//
// 这样测试能够真正证明：
// Decoder 不依赖底层 Read chunk 边界。
type chunkReader struct {
	reader io.Reader

	max int
}

func (
	r *chunkReader,
) Read(
	p []byte,
) (
	int,
	error,
) {
	if len(p) >
		r.max {
		p =
			p[:r.max]
	}

	return r.reader.Read(
		p,
	)
}

func TestDecoderParsesEventAcrossTinyReadChunks(
	t *testing.T,
) {
	input :=
		": keepalive\r\n" +
			"id: 7\r\n" +
			"event: delta\r\n" +
			"data: one\r\n" +
			"data: two\r\n" +
			"\r\n" +
			"data: done\r\n" +
			"\r\n"

	decoder, err :=
		NewDecoder(
			&chunkReader{
				reader: strings.NewReader(
					input,
				),

				max: 3,
			},
			0,
			0,
		)

	if err != nil {
		t.Fatal(err)
	}

	first, err :=
		decoder.Next()

	if err != nil {
		t.Fatal(err)
	}

	if first.Type !=
		"delta" {
		t.Fatalf(
			"type = %q",
			first.Type,
		)
	}

	if first.ID !=
		"7" {
		t.Fatalf(
			"id = %q",
			first.ID,
		)
	}

	if first.Data !=
		"one\ntwo" {
		t.Fatalf(
			"data = %q",
			first.Data,
		)
	}

	second, err :=
		decoder.Next()

	if err != nil {
		t.Fatal(err)
	}

	if second.Type !=
		"message" {
		t.Fatalf(
			"type = %q",
			second.Type,
		)
	}

	// SSE last event id 会继续保留。
	if second.ID !=
		"7" {
		t.Fatalf(
			"id = %q",
			second.ID,
		)
	}

	if second.Data !=
		"done" {
		t.Fatalf(
			"data = %q",
			second.Data,
		)
	}

	_, err =
		decoder.Next()

	if !errors.Is(
		err,
		io.EOF,
	) {
		t.Fatalf(
			"want EOF, got %v",
			err,
		)
	}
}

func TestDecoderReturnsFinalEventWithoutTrailingBlankLine(
	t *testing.T,
) {
	decoder, err :=
		NewDecoder(
			strings.NewReader(
				"data: final",
			),
			0,
			0,
		)

	if err != nil {
		t.Fatal(err)
	}

	event, err :=
		decoder.Next()

	if err != nil {
		t.Fatal(err)
	}

	if event.Data !=
		"final" {
		t.Fatalf(
			"data = %q",
			event.Data,
		)
	}

	_, err =
		decoder.Next()

	if !errors.Is(
		err,
		io.EOF,
	) {
		t.Fatalf(
			"want EOF, got %v",
			err,
		)
	}
}

func TestDecoderRejectsOversizedEvent(
	t *testing.T,
) {
	decoder, err :=
		NewDecoder(
			strings.NewReader(
				"data: 1234\n"+
					"data: 5678\n"+
					"\n",
			),
			1024,
			6,
		)

	if err != nil {
		t.Fatal(err)
	}

	_, err =
		decoder.Next()

	if !errors.Is(
		err,
		ErrEventTooLarge,
	) {
		t.Fatalf(
			"error = %v, want ErrEventTooLarge",
			err,
		)
	}
}

func TestDecoderIgnoresComment(
	t *testing.T,
) {
	decoder, err :=
		NewDecoder(
			strings.NewReader(
				": ping\n"+
					"data: hello\n"+
					"\n",
			),
			0,
			0,
		)

	if err != nil {
		t.Fatal(err)
	}

	event, err :=
		decoder.Next()

	if err != nil {
		t.Fatal(err)
	}

	if event.Data !=
		"hello" {
		t.Fatalf(
			"data = %q",
			event.Data,
		)
	}
}
