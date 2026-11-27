package proxy

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

const (
	sniffBytes = 256 << 10
	modelField = "model"
)

// Inspect only the bytes consumed while locating model, including parser read-ahead.
// Replay those bytes into the streaming normalizer; never forward an unchecked part header.
func multipartBody(body io.ReadCloser, boundary string) (string, io.ReadCloser, string, error) {
	var prefix bytes.Buffer
	preview := multipart.NewReader(io.TeeReader(io.LimitReader(body, sniffBytes), &prefix), boundary)
	var model string

	for model == "" {
		p, err := preview.NextRawPart()
		if err != nil {
			return "", nil, "", errors.New("model must appear within the first 256 KiB")
		}

		name, err := partName(p)
		if err != nil {
			return "", nil, "", err
		}

		if name != modelField {
			continue
		}

		b, err := io.ReadAll(io.LimitReader(p, 4097))
		if err != nil || p.FileName() != "" || len(b) > 4096 || len(b) == 0 || !validModel(string(b)) {
			return "", nil, "", errors.New("invalid multipart model")
		}

		model = string(b)
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer body.Close()
		reader := multipart.NewReader(io.MultiReader(&prefix, body), boundary)
		seen := false
		buf := make([]byte, 32<<10)

		for {
			part, err := reader.NextRawPart()
			if err == io.EOF {
				err = writer.Close()
				pw.CloseWithError(err)

				return
			}

			var name string
			if err == nil {
				name, err = partName(part)
			}

			if err == nil && name == modelField {
				if seen {
					err = bad(errors.New("duplicate multipart model"))
				}

				seen = true
			}

			if err == nil {
				var dst io.Writer

				dst, err = writer.CreatePart(part.Header)
				if err == nil {
					_, err = io.CopyBuffer(dst, part, buf)
				}
			}

			if err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()

	return model, pr, writer.FormDataContentType(), nil
}

// Use one unambiguous part name on both parses. Alternative dispositions or
// transfer encodings could cause a backend to interpret a different model.
func partName(p *multipart.Part) (string, error) {
	values := p.Header.Values("Content-Disposition")
	if len(values) != 1 {
		return "", bad(errors.New("expected one multipart disposition"))
	}

	kind, params, err := mime.ParseMediaType(values[0])
	if err != nil || kind != "form-data" || params["name"] == "" {
		return "", bad(errors.New("invalid multipart disposition"))
	}

	if params["name"] == modelField && p.Header.Get("Content-Transfer-Encoding") != "" {
		return "", bad(errors.New("encoded model field"))
	}

	return params["name"], nil
}

func validModel(s string) bool {
	return s != "" && s == strings.TrimSpace(s) && strings.IndexFunc(s, func(r rune) bool { return r < 32 || r == 127 }) < 0
}
