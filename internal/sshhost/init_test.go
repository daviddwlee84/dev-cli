package sshhost

import (
	"bytes"
	"testing"
)

func TestInsertRootIncludePreservesBytesAndPlacement(t *testing.T) {
	bom := []byte{0xef, 0xbb, 0xbf}
	for _, test := range []struct {
		name    string
		input   []byte
		want    []byte
		present bool
		newline string
	}{
		{
			name:    "before first Host with BOM CRLF",
			input:   append(append([]byte{}, bom...), []byte("# Host in comment\r\nCompression yes\r\nHost old\r\n")...),
			want:    append(append([]byte{}, bom...), []byte("# Host in comment\r\nCompression yes\r\n"+rootIncludeLine()+"\r\nHost old\r\n")...),
			newline: "\r\n",
		},
		{
			name:    "append after unterminated globals",
			input:   []byte("CanonicalizeHostname no"),
			want:    []byte("CanonicalizeHostname no\n" + rootIncludeLine() + "\n"),
			newline: "\n",
		},
		{
			name:    "empty BOM",
			input:   append([]byte{}, bom...),
			want:    append(append([]byte{}, bom...), []byte(rootIncludeLine()+"\n")...),
			newline: "\n",
		},
		{
			name:    "existing quoted top-level Include",
			input:   []byte("Include \"" + ManagedInclude + "\"\nHost old\n"),
			want:    []byte("Include \"" + ManagedInclude + "\"\nHost old\n"),
			present: true,
			newline: "\n",
		},
		{
			name:    "before earlier foreign Include",
			input:   []byte("Include other.conf\nHost old\n"),
			want:    []byte(rootIncludeLine() + "\nInclude other.conf\nHost old\n"),
			newline: "\n",
		},
		{
			name:    "guarded Include is not success",
			input:   []byte("Host old\n Include " + ManagedInclude + "\n"),
			want:    []byte(rootIncludeLine() + "\nHost old\n Include " + ManagedInclude + "\n"),
			newline: "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, newline, _, present, err := insertRootInclude(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.want) || present != test.present || newline != test.newline {
				t.Fatalf("got %q, present %v, newline %q; want %q, %v, %q", got, present, newline, test.want, test.present, test.newline)
			}
		})
	}
}

func TestInsertRootIncludeRejectsMalformedContent(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("Host \"unterminated\n"),
		[]byte{'H', 'o', 's', 't', ' ', 0, 'x'},
		{0xff, 0xfe},
	} {
		if _, _, _, _, _, err := insertRootInclude(input); err == nil {
			t.Errorf("insertRootInclude(%q) succeeded", input)
		}
	}
}
