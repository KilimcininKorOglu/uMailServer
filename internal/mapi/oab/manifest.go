package oab

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
)

// ManifestInput carries the data the OAB manifest (MS-OXWOAB) references: the
// container GUID, the address-list distinguished name, the sequence number, and
// the compressed and uncompressed sizes of the Full Details and template files.
type ManifestInput struct {
	ContainerGUID string
	OABDN         string
	Sequence      uint32

	FullCompressed     []byte // the .lzx Full Details file
	FullRawSize        int    // its uncompressed size
	TemplateCompressed []byte // the lng<lcid>-<seq>.lzx template file
	TemplateRawSize    int    // its uncompressed size
}

// FullFileName returns the Full Details file name for a sequence number.
func FullFileName(sequence uint32) string {
	return fmt.Sprintf("%d.lzx", sequence)
}

// TemplateFileName returns the display-template file name for a sequence number.
// The 0409 language id is US English, the default Outlook requests.
func TemplateFileName(sequence uint32) string {
	return fmt.Sprintf("lng0409-%d.lzx", sequence)
}

// xmlOAB is the manifest document root (MS-OXWOAB).
type xmlOAB struct {
	XMLName xml.Name `xml:"OAB"`
	OAL     xmlOAL   `xml:"OAL"`
}

type xmlOAL struct {
	ID       string      `xml:"id,attr"`
	DN       string      `xml:"dn,attr"`
	Name     string      `xml:"name,attr"`
	Full     xmlFull     `xml:"Full"`
	Template xmlTemplate `xml:"Template"`
}

type xmlFull struct {
	Seq              uint32 `xml:"seq,attr"`
	Ver              uint32 `xml:"ver,attr"`
	Size             int    `xml:"size,attr"`
	UncompressedSize int    `xml:"uncompressedsize,attr"`
	SHA              string `xml:"SHA,attr"`
	File             string `xml:",chardata"`
}

type xmlTemplate struct {
	Seq              uint32 `xml:"seq,attr"`
	Ver              uint32 `xml:"ver,attr"`
	Size             int    `xml:"size,attr"`
	UncompressedSize int    `xml:"uncompressedsize,attr"`
	SHA              string `xml:"SHA,attr"`
	LangID           string `xml:"langid,attr"`
	Type             string `xml:"type,attr"`
	File             string `xml:",chardata"`
}

// BuildManifest renders the OAB manifest XML (MS-OXWOAB) that points Outlook at
// the Full Details and template files, carrying their sizes and SHA-1 digests
// for integrity checking.
func BuildManifest(in ManifestInput) (string, error) {
	doc := xmlOAB{
		OAL: xmlOAL{
			ID:   in.ContainerGUID,
			DN:   in.OABDN,
			Name: GALName,
			Full: xmlFull{
				Seq:              in.Sequence,
				Ver:              Version4,
				Size:             len(in.FullCompressed),
				UncompressedSize: in.FullRawSize,
				SHA:              sha1Hex(in.FullCompressed),
				File:             FullFileName(in.Sequence),
			},
			Template: xmlTemplate{
				Seq:              in.Sequence,
				Ver:              TemplateVersion,
				Size:             len(in.TemplateCompressed),
				UncompressedSize: in.TemplateRawSize,
				SHA:              sha1Hex(in.TemplateCompressed),
				LangID:           "0409",
				Type:             "windows",
				File:             TemplateFileName(in.Sequence),
			},
		},
	}
	body, err := xml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return xml.Header + string(body), nil
}

// sha1Hex returns the lowercase hex SHA-1 digest of data (MS-OXWOAB specifies
// SHA-1, 40 hex characters).
func sha1Hex(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}
