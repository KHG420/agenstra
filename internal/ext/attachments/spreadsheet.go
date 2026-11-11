package attachments

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// xlsxText reads cached cell values and formulas without executing workbook
// code or following relationships outside this archive. Bounds precede XML IO.
func xlsxText(ctx context.Context, data []byte) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > 512 {
		return "", errors.New("attachment_spreadsheet_invalid")
	}
	files := map[string]*zip.File{}
	var expanded uint64
	for _, file := range archive.File {
		expanded += file.UncompressedSize64
		if file.UncompressedSize64 > 16<<20 || expanded > 32<<20 {
			return "", errors.New("attachment_spreadsheet_too_large")
		}
		files[file.Name] = file
	}
	sheetNames := map[string]string{}
	relations := map[string]string{}
	if file := files["xl/_rels/workbook.xml.rels"]; file != nil {
		err := readXLSXXML(ctx, file, func(_ *xml.Decoder, start xml.StartElement) error {
			if start.Name.Local != "Relationship" {
				return nil
			}
			var id, target string
			for _, attribute := range start.Attr {
				if attribute.Name.Local == "Id" {
					id = attribute.Value
				}
				if attribute.Name.Local == "Target" {
					target = attribute.Value
				}
			}
			if strings.HasPrefix(target, "/") {
				target = strings.TrimPrefix(target, "/")
			} else {
				target = path.Join("xl", target)
			}
			if strings.HasPrefix(target, "xl/worksheets/") {
				relations[id] = target
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	if file := files["xl/workbook.xml"]; file != nil {
		err := readXLSXXML(ctx, file, func(_ *xml.Decoder, start xml.StartElement) error {
			if start.Name.Local != "sheet" {
				return nil
			}
			var id, name string
			for _, attribute := range start.Attr {
				if attribute.Name.Local == "id" {
					id = attribute.Value
				}
				if attribute.Name.Local == "name" {
					name = attribute.Value
				}
			}
			if target := relations[id]; target != "" {
				sheetNames[target] = name
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	shared := []string{}
	if file := files["xl/sharedStrings.xml"]; file != nil {
		err = readXLSXXML(ctx, file, func(decoder *xml.Decoder, start xml.StartElement) error {
			if start.Name.Local != "si" {
				return nil
			}
			var item struct {
				Text string `xml:"t"`
				Runs []struct {
					Text string `xml:"t"`
				} `xml:"r"`
			}
			if err := decoder.DecodeElement(&item, &start); err != nil {
				return err
			}
			for _, run := range item.Runs {
				item.Text += run.Text
			}
			if len(shared) >= 50000 {
				return errors.New("attachment_spreadsheet_too_large")
			}
			shared = append(shared, item.Text)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	var output strings.Builder
	for _, file := range archive.File {
		if !strings.HasPrefix(file.Name, "xl/worksheets/sheet") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		name := sheetNames[file.Name]
		if name == "" {
			name = strings.TrimPrefix(file.Name, "xl/worksheets/")
		}
		fmt.Fprintf(&output, "\n[%s]\n", name)
		err = readXLSXXML(ctx, file, func(decoder *xml.Decoder, start xml.StartElement) error {
			if start.Name.Local != "c" {
				return nil
			}
			var cell struct {
				Ref     string `xml:"r,attr"`
				Type    string `xml:"t,attr"`
				Value   string `xml:"v"`
				Formula string `xml:"f"`
				Inline  struct {
					Text string `xml:"t"`
					Runs []struct {
						Text string `xml:"t"`
					} `xml:"r"`
				} `xml:"is"`
			}
			if err := decoder.DecodeElement(&cell, &start); err != nil {
				return err
			}
			value := cell.Value
			if cell.Type == "s" {
				index, err := strconv.Atoi(value)
				if err != nil || index < 0 || index >= len(shared) {
					return errors.New("attachment_spreadsheet_invalid")
				}
				value = shared[index]
			}
			if cell.Type == "inlineStr" {
				value = cell.Inline.Text
				for _, run := range cell.Inline.Runs {
					value += run.Text
				}
			}
			if cell.Formula != "" {
				value += " (formula=" + cell.Formula + "; cached value)"
			}
			if value != "" {
				fmt.Fprintf(&output, "%s\t%s\n", cell.Ref, value)
			}
			if output.Len() > 8<<20 {
				return errors.New("attachment_document_too_large")
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return output.String(), nil
}

func readXLSXXML(ctx context.Context, file *zip.File, visit func(*xml.Decoder, xml.StartElement) error) (resultErr error) {
	reader, err := file.Open()
	if err != nil {
		return errors.New("attachment_spreadsheet_invalid")
	}
	defer func() { resultErr = errors.Join(resultErr, reader.Close()) }()
	decoder := xml.NewDecoder(io.LimitReader(reader, (16<<20)+1))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("attachment_spreadsheet_invalid")
		}
		if start, ok := token.(xml.StartElement); ok {
			if err := visit(decoder, start); err != nil {
				return err
			}
		}
	}
}
