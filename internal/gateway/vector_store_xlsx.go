package gateway

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
)

const (
	spreadsheetMLNamespace       = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	strictSpreadsheetMLNamespace = "http://purl.oclc.org/ooxml/spreadsheetml/main"
)

func vectorStoreXLSXText(content []byte) ([]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil || len(archive.File) > maxVectorStoreOOXMLEntries {
		return nil, errors.New("XLSX content is invalid or exceeds the archive entry limit")
	}
	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		if files[file.Name] != nil {
			return nil, errors.New("XLSX contains duplicate package parts")
		}
		files[file.Name] = file
	}
	workbook, relationships := files["xl/workbook.xml"], files["xl/_rels/workbook.xml.rels"]
	if workbook == nil || relationships == nil || workbook.UncompressedSize64 > maxVectorStoreParsedContentBytes || relationships.UncompressedSize64 > maxVectorStoreParsedContentBytes {
		return nil, errors.New("XLSX workbook metadata is missing or exceeds 16 MiB")
	}
	declared := workbook.UncompressedSize64
	if relationships.UncompressedSize64 > uint64(maxVectorStoreParsedContentBytes)-declared {
		return nil, errors.New("XLSX package XML exceeds 16 MiB")
	}
	declared += relationships.UncompressedSize64
	targets, sharedName, err := vectorStoreXLSXRelationshipTargets(relationships)
	if err != nil {
		return nil, err
	}
	sheetNames, err := vectorStoreXLSXSheetNames(workbook, targets)
	if err != nil {
		return nil, err
	}
	var shared []string
	if sharedName != "" {
		sharedFile := files[sharedName]
		if sharedFile == nil || sharedFile.UncompressedSize64 > uint64(maxVectorStoreParsedContentBytes)-declared {
			return nil, errors.New("XLSX shared strings are missing or exceed 16 MiB")
		}
		declared += sharedFile.UncompressedSize64
		shared, err = vectorStoreXLSXSharedStrings(sharedFile)
		if err != nil {
			return nil, err
		}
	}
	var text strings.Builder
	seen := make(map[string]struct{}, len(sheetNames))
	for _, name := range sheetNames {
		if _, duplicate := seen[name]; duplicate {
			return nil, errors.New("XLSX contains a duplicate worksheet reference")
		}
		seen[name] = struct{}{}
		sheet := files[name]
		if sheet == nil || sheet.UncompressedSize64 > uint64(maxVectorStoreParsedContentBytes)-declared {
			return nil, errors.New("XLSX worksheet content is missing or exceeds 16 MiB")
		}
		declared += sheet.UncompressedSize64
		sheetText, partErr := vectorStoreXLSXWorksheetText(sheet, shared, maxVectorStoreParsedContentBytes-text.Len())
		if partErr != nil {
			return nil, partErr
		}
		text.Write(sheetText)
	}
	return []byte(text.String()), nil
}

func vectorStoreXLSXRelationshipTargets(file *zip.File) (map[string]string, string, error) {
	decoder, closePart, err := vectorStoreOOXMLDecoder(file)
	if err != nil {
		return nil, "", errors.New("XLSX relationships could not be opened")
	}
	defer closePart()
	targets := make(map[string]string)
	seenIDs := make(map[string]struct{})
	sharedName := ""
	rootSeen := false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			if !rootSeen {
				return nil, "", errors.New("XLSX relationships root is invalid")
			}
			return targets, sharedName, nil
		}
		if tokenErr != nil {
			return nil, "", errors.New("XLSX relationships are not valid XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if start.Name.Local != "Relationships" || (start.Name.Space != packageRelationshipNamespace && start.Name.Space != strictPackageRelationshipNS) {
				return nil, "", errors.New("XLSX relationships root is invalid")
			}
			rootSeen = true
			continue
		}
		if start.Name.Local != "Relationship" || (start.Name.Space != packageRelationshipNamespace && start.Name.Space != strictPackageRelationshipNS) {
			continue
		}
		var id, target, relationshipType, targetMode string
		for _, attribute := range start.Attr {
			switch attribute.Name.Local {
			case "Id":
				id = attribute.Value
			case "Target":
				target = attribute.Value
			case "Type":
				relationshipType = attribute.Value
			case "TargetMode":
				targetMode = attribute.Value
			}
		}
		worksheet := relationshipType == officeRelationshipNamespace+"/worksheet" || relationshipType == strictOfficeRelationshipNS+"/worksheet"
		sharedStrings := relationshipType == officeRelationshipNamespace+"/sharedStrings" || relationshipType == strictOfficeRelationshipNS+"/sharedStrings"
		if !worksheet && !sharedStrings {
			continue
		}
		if id == "" || target == "" || (targetMode != "" && targetMode != "Internal") || strings.Contains(target, "\\") {
			return nil, "", errors.New("XLSX contains an invalid workbook relationship")
		}
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, "", errors.New("XLSX contains duplicate workbook relationship IDs")
		}
		seenIDs[id] = struct{}{}
		name := path.Clean(path.Join("xl", strings.TrimPrefix(target, "/xl/")))
		if worksheet {
			if !strings.HasPrefix(name, "xl/worksheets/") || path.Ext(name) != ".xml" || targets[id] != "" {
				return nil, "", errors.New("XLSX contains an invalid worksheet relationship")
			}
			targets[id] = name
			continue
		}
		if name != "xl/sharedStrings.xml" || sharedName != "" {
			return nil, "", errors.New("XLSX contains an invalid shared-strings relationship")
		}
		sharedName = name
	}
}

func vectorStoreXLSXSheetNames(file *zip.File, targets map[string]string) ([]string, error) {
	decoder, closePart, err := vectorStoreOOXMLDecoder(file)
	if err != nil {
		return nil, errors.New("XLSX workbook could not be opened")
	}
	defer closePart()
	var sheets []string
	rootSeen := false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			if !rootSeen || len(sheets) == 0 {
				return nil, errors.New("XLSX workbook root or worksheets are invalid")
			}
			return sheets, nil
		}
		if tokenErr != nil {
			return nil, errors.New("XLSX workbook is not valid XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if start.Name.Local != "workbook" || (start.Name.Space != spreadsheetMLNamespace && start.Name.Space != strictSpreadsheetMLNamespace) {
				return nil, errors.New("XLSX workbook root is invalid")
			}
			rootSeen = true
			continue
		}
		if !vectorStoreOOXMLElement(start.Name, "sheet", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
			continue
		}
		var relationshipID string
		for _, attribute := range start.Attr {
			if attribute.Name.Local == "id" && (attribute.Name.Space == officeRelationshipNamespace || attribute.Name.Space == strictOfficeRelationshipNS) {
				relationshipID = attribute.Value
			}
		}
		if targets[relationshipID] == "" {
			return nil, errors.New("XLSX worksheet order references a missing relationship")
		}
		sheets = append(sheets, targets[relationshipID])
	}
}

func vectorStoreXLSXSharedStrings(file *zip.File) ([]string, error) {
	decoder, closePart, err := vectorStoreOOXMLDecoder(file)
	if err != nil {
		return nil, errors.New("XLSX shared strings could not be opened")
	}
	defer closePart()
	var values []string
	rootSeen := false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			if !rootSeen {
				return nil, errors.New("XLSX shared-strings root is invalid")
			}
			return values, nil
		}
		if tokenErr != nil {
			return nil, errors.New("XLSX shared strings are not valid XML")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if start.Name.Local != "sst" || (start.Name.Space != spreadsheetMLNamespace && start.Name.Space != strictSpreadsheetMLNamespace) {
				return nil, errors.New("XLSX shared-strings root is invalid")
			}
			rootSeen = true
			continue
		}
		if vectorStoreOOXMLElement(start.Name, "si", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
			value, valueErr := vectorStoreXLSXTextElement(decoder, start)
			if valueErr != nil {
				return nil, valueErr
			}
			values = append(values, value)
		}
	}
}

func vectorStoreXLSXWorksheetText(file *zip.File, shared []string, maxText int) ([]byte, error) {
	decoder, closePart, err := vectorStoreOOXMLDecoder(file)
	if err != nil {
		return nil, errors.New("XLSX worksheet could not be opened")
	}
	defer closePart()
	var text strings.Builder
	rootSeen, inRow, cellSeen := false, false, false
	for {
		token, tokenErr := decoder.Token()
		if errors.Is(tokenErr, io.EOF) {
			if !rootSeen {
				return nil, errors.New("XLSX worksheet root is invalid")
			}
			return []byte(text.String()), nil
		}
		if tokenErr != nil {
			return nil, errors.New("XLSX worksheet is not valid XML")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if !rootSeen {
				if value.Name.Local != "worksheet" || (value.Name.Space != spreadsheetMLNamespace && value.Name.Space != strictSpreadsheetMLNamespace) {
					return nil, errors.New("XLSX worksheet root is invalid")
				}
				rootSeen = true
				continue
			}
			if vectorStoreOOXMLElement(value.Name, "row", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
				inRow, cellSeen = true, false
				continue
			}
			if inRow && vectorStoreOOXMLElement(value.Name, "c", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
				cell, cellErr := vectorStoreXLSXCellText(decoder, value, shared)
				if cellErr != nil {
					return nil, cellErr
				}
				separator := 0
				if cellSeen {
					separator = 1
				}
				if text.Len()+separator+len(cell) > maxText {
					return nil, errors.New("XLSX text exceeds 16 MiB")
				}
				if cellSeen {
					text.WriteByte('\t')
				}
				text.WriteString(cell)
				cellSeen = true
			}
		case xml.EndElement:
			if inRow && vectorStoreOOXMLElement(value.Name, "row", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
				if cellSeen {
					if text.Len()+1 > maxText {
						return nil, errors.New("XLSX text exceeds 16 MiB")
					}
					text.WriteByte('\n')
				}
				inRow, cellSeen = false, false
			}
		}
	}
}

func vectorStoreXLSXCellText(decoder *xml.Decoder, start xml.StartElement, shared []string) (string, error) {
	cellType := ""
	for _, attribute := range start.Attr {
		if attribute.Name.Local == "t" {
			cellType = attribute.Value
		}
	}
	depth := 1
	value, inline := "", strings.Builder{}
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return "", errors.New("XLSX cell is not valid XML")
		}
		switch token := token.(type) {
		case xml.StartElement:
			if vectorStoreOOXMLElement(token.Name, "v", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
				if decoder.DecodeElement(&value, &token) != nil {
					return "", errors.New("XLSX cell value is invalid")
				}
				continue
			}
			if vectorStoreOOXMLElement(token.Name, "t", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
				var fragment string
				if decoder.DecodeElement(&fragment, &token) != nil || inline.Len()+len(fragment) > maxVectorStoreParsedContentBytes {
					return "", errors.New("XLSX inline text is invalid or exceeds 16 MiB")
				}
				inline.WriteString(fragment)
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if cellType != "s" {
		if cellType == "inlineStr" {
			return inline.String(), nil
		}
		return value, nil
	}
	index, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || index < 0 || index >= len(shared) {
		return "", errors.New("XLSX cell references an invalid shared string")
	}
	return shared[index], nil
}

func vectorStoreXLSXTextElement(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	depth := 1
	var text strings.Builder
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return "", errors.New("XLSX shared string is not valid XML")
		}
		switch token := token.(type) {
		case xml.StartElement:
			if vectorStoreOOXMLElement(token.Name, "t", spreadsheetMLNamespace, strictSpreadsheetMLNamespace) {
				var fragment string
				if decoder.DecodeElement(&fragment, &token) != nil || text.Len()+len(fragment) > maxVectorStoreParsedContentBytes {
					return "", errors.New("XLSX shared string is invalid or exceeds 16 MiB")
				}
				text.WriteString(fragment)
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return text.String(), nil
}
