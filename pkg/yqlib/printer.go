package yqlib

import (
	"bufio"
	"bytes"
	"container/list"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type Printer interface {
	PrintResults(matchingNodes *list.List) error
	PrintedAnything() bool
	//e.g. when given a front-matter doc, like jekyll
	SetAppendix(reader io.Reader)
	SetNulSepOutput(nulSepOutput bool)
}

type EncoderFactoryFunction func() Encoder

type PrinterOutputFormat struct {
	FormalName     string
	Names          []string
	EncoderFactory EncoderFactoryFunction
}

var YamlOutputFormat = &PrinterOutputFormat{"yaml", []string{"y", "yml"}, func() Encoder { return NewYamlEncoder(ConfiguredYamlPreferences) }}
var JSONOutputFormat = &PrinterOutputFormat{"json", []string{"j"}, func() Encoder { return NewJSONEncoder(ConfiguredJSONPreferences) }}
var PropsOutputFormat = &PrinterOutputFormat{"props", []string{"p", "properties"}, func() Encoder { return NewPropertiesEncoder(ConfiguredPropertiesPreferences) }}
var CSVOutputFormat = &PrinterOutputFormat{"csv", []string{"c"}, func() Encoder { return NewCsvEncoder(ConfiguredCsvPreferences) }}
var TSVOutputFormat = &PrinterOutputFormat{"tsv", []string{"t"}, func() Encoder { return NewCsvEncoder(ConfiguredTsvPreferences) }}
var XMLOutputFormat = &PrinterOutputFormat{"xml", []string{"x"}, func() Encoder { return NewXMLEncoder(ConfiguredXMLPreferences) }}

var Base64OutputFormat = &PrinterOutputFormat{}
var UriOutputFormat = &PrinterOutputFormat{}
var ShOutputFormat = &PrinterOutputFormat{}

var TomlOutputFormat = &PrinterOutputFormat{"toml", []string{}, func() Encoder { return NewTomlEncoder() }}
var ShellVariablesOutputFormat = &PrinterOutputFormat{"shell", []string{"s", "sh"}, func() Encoder { return NewShellVariablesEncoder() }}

var LuaOutputFormat = &PrinterOutputFormat{"lua", []string{"l"}, func() Encoder { return NewLuaEncoder(ConfiguredLuaPreferences) }}

var Formats = []*PrinterOutputFormat{
	YamlOutputFormat,
	JSONOutputFormat,
	PropsOutputFormat,
	CSVOutputFormat,
	TSVOutputFormat,
	XMLOutputFormat,
	Base64OutputFormat,
	UriOutputFormat,
	ShOutputFormat,
	TomlOutputFormat,
	ShellVariablesOutputFormat,
	LuaOutputFormat,
}

func (f *PrinterOutputFormat) MatchesName(name string) bool {
	if f.FormalName == name {
		return true
	}
	for _, n := range f.Names {
		if n == name {
			return true
		}
	}
	return false
}

func (f *PrinterOutputFormat) GetConfiguredEncoder() Encoder {
	return f.EncoderFactory()
}

func OutputFormatFromString(format string) (*PrinterOutputFormat, error) {
	for _, printerFormat := range Formats {
		if printerFormat.MatchesName(format) {
			return printerFormat, nil
		}
	}

	return nil, fmt.Errorf("unknown format '%v' please use [%v]", format, GetAvailableOutputFormatString())
}

func GetAvailableOutputFormatString() string {
	var formats = []string{}
	for _, printerFormat := range Formats {
		if printerFormat.FormalName != "" {
			formats = append(formats, printerFormat.FormalName)
		}
		if len(printerFormat.Names) >= 1 {
			formats = append(formats, printerFormat.Names[0])
		}
	}
	return strings.Join(formats, "|")
}

type resultsPrinter struct {
	encoder           Encoder
	printerWriter     PrinterWriter
	firstTimePrinting bool
	previousDocIndex  uint
	previousFileIndex int
	printedMatches    bool
	treeNavigator     DataTreeNavigator
	appendixReader    io.Reader
	nulSepOutput      bool
}

func NewPrinter(encoder Encoder, printerWriter PrinterWriter) Printer {
	return &resultsPrinter{
		encoder:           encoder,
		printerWriter:     printerWriter,
		firstTimePrinting: true,
		treeNavigator:     NewDataTreeNavigator(),
		nulSepOutput:      false,
	}
}

func (p *resultsPrinter) SetNulSepOutput(nulSepOutput bool) {
	log.Debug("Setting NUL separator output")

	p.nulSepOutput = nulSepOutput
}

func (p *resultsPrinter) SetAppendix(reader io.Reader) {
	p.appendixReader = reader
}

func (p *resultsPrinter) PrintedAnything() bool {
	return p.printedMatches
}

func (p *resultsPrinter) printNode(node *CandidateNode, writer io.Writer) error {
	p.printedMatches = p.printedMatches || (node.Tag != "!!null" &&
		(node.Tag != "!!bool" || node.Value != "false"))
	return p.encoder.Encode(writer, node)
}

func removeLastEOL(b *bytes.Buffer) {
	data := b.Bytes()
	n := len(data)
	if n >= 2 && data[n-2] == '\r' && data[n-1] == '\n' {
		b.Truncate(n - 2)
	} else if n >= 1 && (data[n-1] == '\r' || data[n-1] == '\n') {
		b.Truncate(n - 1)
	}
}

func (p *resultsPrinter) PrintResults(matchingNodes *list.List) error {
	log.Debug("PrintResults for %v matches", matchingNodes.Len())

	if matchingNodes.Len() == 0 {
		log.Debug("no matching results, nothing to print")
		return nil
	}

	if !p.encoder.CanHandleAliases() {
		explodeOp := Operation{OperationType: explodeOpType}
		explodeNode := ExpressionNode{Operation: &explodeOp}
		context, err := p.treeNavigator.GetMatchingNodes(Context{MatchingNodes: matchingNodes}, &explodeNode)
		if err != nil {
			return err
		}
		matchingNodes = context.MatchingNodes
	}

	if p.firstTimePrinting {
		node := matchingNodes.Front().Value.(*CandidateNode)
		p.previousDocIndex = node.GetDocument()
		p.previousFileIndex = node.GetFileIndex()
		p.firstTimePrinting = false
	}

	for el := matchingNodes.Front(); el != nil; el = el.Next() {

		mappedDoc := el.Value.(*CandidateNode)
		log.Debug("print sep logic: p.firstTimePrinting: %v, previousDocIndex: %v", p.firstTimePrinting, p.previousDocIndex)
		log.Debug("%v", NodeToString(mappedDoc))
		writer, errorWriting := p.printerWriter.GetWriter(mappedDoc)
		if errorWriting != nil {
			return errorWriting
		}

		commentsStartWithSepExp := regexp.MustCompile(`^\$yqDocSeparator\$`)
		commentStartsWithSeparator := commentsStartWithSepExp.MatchString(mappedDoc.LeadingContent)

		if (p.previousDocIndex != mappedDoc.GetDocument() || p.previousFileIndex != mappedDoc.GetFileIndex()) && !commentStartsWithSeparator {
			if err := p.encoder.PrintDocumentSeparator(writer); err != nil {
				return err
			}
		}

		var destination io.Writer = writer
		tempBuffer := bytes.NewBuffer(nil)
		if p.nulSepOutput {
			destination = tempBuffer
		}

		if err := p.encoder.PrintLeadingContent(destination, mappedDoc.LeadingContent); err != nil {
			return err
		}

		if err := p.printNode(mappedDoc, destination); err != nil {
			return err
		}

		if p.nulSepOutput {
			removeLastEOL(tempBuffer)
			tempBufferBytes := tempBuffer.Bytes()
			if bytes.IndexByte(tempBufferBytes, 0) != -1 {
				return fmt.Errorf(
					"Can't serialize value because it contains NUL char and you are using NUL separated output",
				)
			}
			if _, err := writer.Write(tempBufferBytes); err != nil {
				return err
			}
			if _, err := writer.Write([]byte{0}); err != nil {
				return err
			}
		}

		p.previousDocIndex = mappedDoc.GetDocument()
		if err := writer.Flush(); err != nil {
			return err
		}
		log.Debugf("done printing results")
	}

	// what happens if I remove output format check?
	if p.appendixReader != nil {
		writer, err := p.printerWriter.GetWriter(nil)
		if err != nil {
			return err
		}

		log.Debug("Piping appendix reader...")
		betterReader := bufio.NewReader(p.appendixReader)
		_, err = io.Copy(writer, betterReader)
		if err != nil {
			return err
		}
		if err := writer.Flush(); err != nil {
			return err
		}
	}

	return nil
}
