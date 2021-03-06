package sstable

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/zl14917/MastersProject/kvstore/blockstore"
	"github.com/zl14917/MastersProject/kvstore/types"
	"io"
	"os"
	"path"
	"strconv"
)

type EntryFlags uint32
type DataFileFlags uint32

const (
	HeaderUninitialized uint32 = 0x77777777
	BaseBlockSize       uint32 = 1024 * 4
)

const (
	KeyExists uint32 = (1 << iota)
	KeyDeleted
)

var KeyTooLarge = errors.New("SSTable key too large to be stored")
var ValueTooLarge = errors.New("SSTable value too large to be stored")

type SSTable struct {
	IndexFilePath string
	DataFilePath  string

	MaxKeySize   int
	MaxValueSize int

	InMem                 bool
	IndexStorageBlockSize int
	DataStoreBlockSize    int

	dataStorage  blockstore.BlockStorage
	indexStorage blockstore.BlockStorage

	CreatedTimestamp uint64

	loadExisting bool
}

type SSTableIO interface {
	NewReader() SSTableReader
	NewWriter() SSTableWriter
}

const indexFileName = "index"
const dataFileName = "data"

type SSTableOpenOptions struct {
	Prefix         string
	MaxKeySize     int
	MaxValueSize   int
	IndexBlockSize int
	DataBlockSize  int
	InMemStore     bool
	LoadExisting   bool
	Timestamp      int64
}

var defaultSSTableOpenOptions = SSTableOpenOptions{
	Prefix:         "level_0_",
	MaxKeySize:     4 * 1024,
	MaxValueSize:   16 * 1024,
	IndexBlockSize: 4 * 1024,
	DataBlockSize:  16 * 1024,
}

func NewSSTable(dirPath string, options *SSTableOpenOptions) (*SSTable) {

	timeStr := strconv.FormatInt(options.Timestamp, 10)
	sstable := &SSTable{
		IndexFilePath: path.Join(dirPath, fmt.Sprintf("%s%s_%s", options.Prefix, timeStr, indexFileName)),
		DataFilePath:  path.Join(dirPath, fmt.Sprintf("%s%s_%s", options.Prefix, timeStr, dataFileName)),
		InMem:         options.InMemStore,
		MaxKeySize:    options.MaxKeySize,
		MaxValueSize:  options.MaxValueSize,

		IndexStorageBlockSize: options.IndexBlockSize,
		DataStoreBlockSize:    options.DataBlockSize,

		indexStorage: nil,
		dataStorage:  nil,
		loadExisting: options.LoadExisting,
	}

	return sstable
}

func (s *SSTable) openStorage(useInMem bool, blockSize int, path string) (blockstore.BlockStorage, error) {
	var (
		store blockstore.BlockStorage
		err   error
	)

	if useInMem {
		store = blockstore.NewInMemBlockStorage(blockSize)

		return store, nil
	}

	if s.loadExisting {
		store, err = blockstore.OpenBlockFile(
			path,
			blockstore.WithBlockSize(blockSize),
		)
	} else {
		store, err = blockstore.NewBlockFile(
			path,
			blockstore.WithBlockSize(blockSize),
		)
	}

	if err != nil {
		return nil, err
	}

	return store, err
}

func (s *SSTable) createOrOpenIndexStorage() error {
	var err error
	if s.indexStorage == nil {
		s.indexStorage, err = s.openStorage(s.InMem, s.IndexStorageBlockSize, s.IndexFilePath)
		if err != nil {
			s.indexStorage = nil
			return err
		}
	}

	return nil
}

func (s *SSTable) createOrOpenDataStorage() error {
	var err error
	if s.dataStorage == nil {
		s.dataStorage, err = s.openStorage(s.InMem, s.DataStoreBlockSize, s.DataFilePath)
		if err != nil {
			s.dataStorage = nil
			return err
		}
	}

	return nil
}

func LoadSSTableFrom(dirPath string, options *SSTableOpenOptions) *SSTable {
	options.LoadExisting = true
	return NewSSTable(dirPath, options)
}

func (s *SSTable) NewReader() (SSTableReader, error) {
	err := s.createOrOpenDataStorage()
	if err != nil {
		return nil, err
	}

	err = s.createOrOpenIndexStorage()

	if err != nil {
		return nil, err
	}

	reader := &sstableReaderStruct{
		indexReader: newSSTableIndexReader(s.indexStorage),
		dataReader:  newSSTableDataReader(s.dataStorage),
	}

	return reader, nil
}

func (s *SSTable) NewWriter() (SSTableWriter, error) {

	writer := &sstableWriterStruct{
		sstableDataWriter:       newSStableDataWriter(s.dataStorage),
		sstableBlockIndexWriter: newSSTableIndexWriter(s.indexStorage),
	}

	err := writer.sstableDataWriter.WriteHeader()
	if err != nil {
		return nil, fmt.Errorf("error writing data file header: %v", err)
	}

	err = writer.sstableBlockIndexWriter.WriteHeader()

	if err != nil {
		return nil, fmt.Errorf("error writing index file header: %v", err)
	}
	return writer, nil
}

func (s *SSTable) Close() error {
	var errIndex, errData error
	if s.indexStorage != nil {
		errIndex = s.indexStorage.Close()

		if errIndex == nil {
			s.indexStorage = nil
		}
	}
	if s.dataStorage != nil {
		errData = s.dataStorage.Close()
		if errData == nil {
			s.dataStorage = nil
		}
	}

	if errIndex != nil || errData != nil {
		return fmt.Errorf("error closing storages: %v, %v", errIndex, errData)
	}

	return nil
}

func (s *SSTable) PermanentlyRemove() error {
	err := s.Close()

	if err != nil {
		return err
	}

	err = os.Remove(s.DataFilePath)
	if err != nil {
		return err
	}

	err = os.Remove(s.IndexFilePath)

	if err != nil {
		return err
	}

	s.indexStorage = nil
	s.dataStorage = nil
	return nil
}

type sstableBlockIndexWriter struct {
	Storage   blockstore.BlockStorage
	BlockSize int

	keyCount int

	currentBlockIndex uint
	blockKeyCount     uint
	MaxKeySize        int
	fileMetaData      SSTableIndexFile

	entryMarshallBuffer *bytes.Buffer
	blockBuffer         *bytes.Buffer

	header SSTableIndexFileHeader
}

func newSSTableIndexWriter(storage blockstore.BlockStorage) sstableBlockIndexWriter {
	return sstableBlockIndexWriter{
		Storage:             storage,
		BlockSize:           storage.BlockSize(),
		MaxKeySize:          MaxKeySizeFitInBlocK(storage.BlockSize()),
		blockBuffer:         bytes.NewBuffer(nil),
		entryMarshallBuffer: bytes.NewBuffer(nil),

		currentBlockIndex: 1,
	}
}

func (w *sstableBlockIndexWriter) WriteHeader() error {
	var err error

	_, err = w.Storage.Allocate(1)
	if err != nil {
		return err
	}

	buffer := bytes.NewBuffer(nil)
	err = w.header.Marshall(buffer)

	if err != nil {
		return err
	}

	_, err = w.Storage.WriteBlock(0, buffer)

	if err != nil {
		return err
	}

	return nil
}

func (w *sstableBlockIndexWriter) Commit() error {
	err := w.FlushCurrentBlock()
	if err != nil {
		return err
	}

	w.header.Magic = IndexFileMagic
	w.header.MaxKeySize = uint32(w.MaxKeySize)
	w.header.BlockSize = uint32(w.BlockSize)
	w.header.Flags = IndexFileFlags(0)
	w.header.BlockCount = uint32(w.currentBlockIndex - 1)
	w.header.KeyCount = uint32(w.keyCount)

	err = w.WriteHeader()

	if err != nil {
		return err
	}

	err = w.Storage.Sync()
	return err
}

func (writer *sstableBlockIndexWriter) WriteIndex(key types.KeyType, deleted bool, position blockstore.Position) error {

	if len(key) > writer.MaxKeySize {
		return fmt.Errorf("can't write key: %v, Key Size : %d", KeyTooLarge, len(key))
	}

	flags := SSTableIndexKeyInsert

	if deleted {
		flags = SSTableIndexKeyDelete
		position = blockstore.UninitializedPosition
	}

	entry := SSTableIndexEntry{
		Flags:          flags,
		KeyLen:         uint32(len(key)),
		DataFileOffSet: position.EncodeUint64(),
		LargeKey:       key,
	}

	fmt.Printf("%v", entry)
	writer.entryMarshallBuffer.Reset()
	if writer.blockKeyCount == 0 {
		writer.entryMarshallBuffer.WriteByte(0x7f)
		writer.entryMarshallBuffer.WriteByte(0x7f)
		writer.entryMarshallBuffer.WriteByte(0x7f)
		writer.entryMarshallBuffer.WriteByte(0x7f)
	}

	_, err := entry.Marshall(writer.entryMarshallBuffer)

	if err != nil {
		return err
	}

	serializedBytes := writer.entryMarshallBuffer.Bytes()

	if writer.blockBuffer.Len()+len(serializedBytes) >= writer.BlockSize {
		err = writer.FlushCurrentBlock()
		if err != nil {
			return err
		}
	}

	_, err = writer.blockBuffer.Write(serializedBytes)

	if err != nil {
		return err
	}
	writer.blockKeyCount++
	writer.keyCount++

	return nil
}

func (writer *sstableBlockIndexWriter) FlushCurrentBlock() error {
	var err error

	bs := writer.blockBuffer.Bytes()

	if len(bs) < 1 {
		return nil
	}

	binary.BigEndian.PutUint32(bs[0:4], uint32(writer.blockKeyCount))
	_, err = writer.Storage.Allocate(int(writer.currentBlockIndex + 1))

	if err != nil {
		return err
	}

	_, err = writer.Storage.WriteBlock(writer.currentBlockIndex, writer.blockBuffer)

	if err != nil {
		return err
	}

	writer.blockKeyCount = 0
	writer.blockBuffer.Reset()
	writer.currentBlockIndex++

	return nil
}

type sstableDataWriter struct {
	Storage      blockstore.BlockStorage
	BlockSize    int
	MaxValueSize int
	recordCount  int

	recordWriteBuffer *bytes.Buffer
	header            SSTableDataFileHeader
}

func (writer *sstableDataWriter) WriteValue(value types.ValueType) (index blockstore.Position, err error) {

	if len(value) > writer.MaxValueSize {
		return blockstore.UninitializedPosition, ValueTooLarge
	}
	beforePosition := writer.Storage.WritePosition()

	writer.recordWriteBuffer.Reset()
	record := SSTableDataRecord{
		ValueLen: uint32(len(value)),
		Value:    value,
	}

	err = record.Marshall(writer.recordWriteBuffer)
	if err != nil {
		return
	}

	_, err = writer.Storage.Write(value)

	if err != nil {
		return blockstore.UninitializedPosition, err
	}
	afterPosition := writer.Storage.WritePosition()

	if beforePosition.Block == afterPosition.Block {
		return beforePosition, nil
	}
	writer.recordCount++

	return blockstore.Position{
		Block:  afterPosition.Block,
		Offset: 0,
	}, nil
}
func newSStableDataWriter(storage blockstore.BlockStorage) sstableDataWriter {
	blockSize := storage.BlockSize()

	writer := sstableDataWriter{
		Storage:      storage,
		BlockSize:    blockSize,
		MaxValueSize: MaxValueSizeFitInBlock(blockSize),

		recordWriteBuffer: bytes.NewBuffer(nil),
		header:            UnitializedSSTableDataFileHeader,
	}

	position := writer.Storage.WritePosition()

	if position.Block == 0 {
		padHeaderBlock := writer.Storage.BlockSize() - position.Offset
		zeros := make([]byte, padHeaderBlock, padHeaderBlock)
		_, err := writer.Storage.Write(zeros)

		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
	}

	return writer
}

func (w *sstableDataWriter) WriteHeader() error {
	w.recordWriteBuffer.Reset()
	err := w.header.Marshall(w.recordWriteBuffer)

	if err != nil {
		return err
	}
	_, err = w.Storage.WriteBlock(0, w.recordWriteBuffer)

	return err
}

func (w *sstableDataWriter) Commit() error {

	header := &w.header

	header.BlockCount = uint32(w.Storage.NumBlocks())
	header.BlockSize = uint32(w.BlockSize)
	header.ValuesCount = uint32(w.recordCount)

	err := w.WriteHeader()
	if err != nil {
		return err
	}

	return w.Storage.Sync()
}

type sstableWriterStruct struct {
	sstableDataWriter
	sstableBlockIndexWriter
	writeBuffer *bytes.Buffer
}

func (w *sstableWriterStruct) MaxKeySize() int {
	return w.sstableBlockIndexWriter.MaxKeySize
}

func (w *sstableWriterStruct) MaxValueSize() int {
	return w.sstableDataWriter.MaxValueSize
}

func (w *sstableWriterStruct) Write(key types.KeyType, value types.ValueType, deleted bool) error {
	var (
		keyLen   = len(key)
		valueLen = len(value)

		position = blockstore.UninitializedPosition
		err      error
	)

	if w.sstableBlockIndexWriter.MaxKeySize < keyLen {
		return KeyTooLarge
	}

	if w.sstableDataWriter.MaxValueSize < valueLen {
		return ValueTooLarge
	}

	if !deleted {
		position, err = w.WriteValue(value)
	}

	if err != nil {
		return err
	}

	err = w.WriteIndex(key, deleted, position)

	if err != nil {
		return err
	}

	return err
}

func (w *sstableWriterStruct) Commit() error {
	err := w.sstableBlockIndexWriter.FlushCurrentBlock()

	if err != nil {
		return err
	}

	err = w.sstableBlockIndexWriter.Commit()

	if err != nil {
		return err
	}

	err = w.sstableDataWriter.Commit()

	return nil
}

type sstableIndexReader struct {
	blockFirstKeyCache map[uint]*SSTableIndexEntry

	indexStorage blockstore.BlockStorage
	header       SSTableIndexFileHeader
	buffer       *bytes.Buffer
}

func newSSTableIndexReader(storage blockstore.BlockStorage) sstableIndexReader {
	return sstableIndexReader{
		indexStorage:       storage,
		blockFirstKeyCache: make(map[uint]*SSTableIndexEntry),

		header: UnitialzedSSTableIndexFileHeader,
		buffer: bytes.NewBuffer(nil),
	}
}

func (r *sstableIndexReader) ReadHeader() error {
	r.buffer.Reset()
	_, err := r.indexStorage.ReadBlock(0, r.buffer)

	if err != nil {
		return err
	}

	err = r.header.UnMarshall(r.buffer)
	return err
}

func (r *sstableIndexReader) readFirstEntryOfBlock(n uint) (entry *SSTableIndexEntry, err error) {
	r.buffer.Reset()
	_, err = r.indexStorage.ReadBlock(n, r.buffer)

	if err != nil {
		return nil, err
	}

	indexBlock := SSTableIndexBlock{}
	err = indexBlock.UnMarshall(r.buffer)

	if err != nil {
		return nil, err
	}

	if indexBlock.KeyCount < 1 {
		return nil, IndexBlockEmptyErr
	}

	entry = &SSTableIndexEntry{}

	err = entry.UnMarshall(r.buffer)
	if err != nil {
		return nil, err
	}

	return entry, nil
}

func (r *sstableIndexReader) getFirstEntryOfBlock(n uint) (entry *SSTableIndexEntry, err error) {
	entry, ok := r.blockFirstKeyCache[n]
	if !ok {
		entry, err = r.readFirstEntryOfBlock(n)
		if err != nil {
			return nil, err
		}

		r.blockFirstKeyCache[n] = entry
	}

	return entry, nil
}

func (r *sstableIndexReader) FindIndexForKey(key types.KeyType) (entry *SSTableIndexEntry, ok bool, err error) {
	// binary search
	var (
		mid   uint
		left  uint = 1
		right uint = uint(r.header.BlockCount)
	)

	for left < right {
		mid = left + (right-left)/2
		entry, err = r.getFirstEntryOfBlock(mid)

		if err != nil {
			return nil, false, err
		}

		cmp := bytes.Compare(key, entry.LargeKey)

		if cmp == 0 {
			return entry, true, nil
		} else if cmp < 0 {
			right = mid
		} else {
			left = mid + 1
		}
	}

	if left == right {
		entry, err = r.getFirstEntryOfBlock(left)
		if err != nil {
			return nil, false, err
		}

		if bytes.Compare(entry.LargeKey, key) <= 0 {
			return r.searchForKeyInBlock(key, left)
		} else {
			return nil, false, nil
		}
	}

	return nil, false, nil
}

func (r *sstableIndexReader) searchForKeyInBlock(key types.KeyType, block uint) (entry *SSTableIndexEntry, ok bool, err error) {
	r.buffer.Reset()
	_, err = r.indexStorage.ReadBlock(block, r.buffer)

	if err != nil {
		return nil, false, err
	}

	indexBlock := SSTableIndexBlock{}
	err = indexBlock.UnMarshall(r.buffer)

	if err != nil {
		return nil, false, err
	}
	entry = &SSTableIndexEntry{}

	var i uint32 = 0
	for ; i < indexBlock.KeyCount; i++ {
		err = entry.UnMarshall(r.buffer)
		if err == io.EOF {
			return nil, false, nil
		}

		if err != nil {
			return nil, false, err
		}

		cmp := bytes.Compare(entry.LargeKey, key)
		if cmp == 0 {
			return entry, true, nil
		} else if cmp > 0 {
			return nil, false, nil
		}
	}
	return nil, false, nil
}

type sstableDataReader struct {
	storage blockstore.BlockStorage
	buffer  *bytes.Buffer

	header SSTableDataFileHeader
}

func newSSTableDataReader(storage blockstore.BlockStorage) sstableDataReader {
	return sstableDataReader{
		buffer:  bytes.NewBuffer(nil),
		storage: storage,
	}
}

func (r *sstableDataReader) ReaderHeader() error {
	r.buffer.Reset()
	_, err := r.storage.ReadBlock(0, r.buffer)
	if err != nil {
		return err
	}

	err = r.header.UnMarshall(r.buffer)
	if err != nil {
		return err
	}

	return nil
}

// random access read
func (r *sstableDataReader) ReadValueAt(position blockstore.Position) (types.ValueType, error) {
	r.buffer.Reset()
	n, err := r.storage.ReadBlock(uint(position.Block), r.buffer)
	if err != nil {
		return nil, err
	}

	if n <= position.Offset {
		return nil, io.EOF
	}

	r.buffer.Next(position.Offset)

	record := &SSTableDataRecord{}
	err = record.UnMarshall(r.buffer)

	if err != nil {
		return nil, err
	}

	return record.Value, nil
}

type sstableReaderStruct struct {
	indexReader sstableIndexReader
	dataReader  sstableDataReader
}

func (r *sstableReaderStruct) ReadNext() (key types.KeyType, value types.ValueType, deleted bool, err error) {
	return
}

func (r *sstableReaderStruct) FindRecord(key types.KeyType) (value types.ValueType, deleted bool, ok bool, err error) {
	entry, ok, err := r.indexReader.FindIndexForKey(key)
	// error
	if err != nil {
		return nil, false, false, err
	}
	// not found
	if ! ok || entry == nil {
		return nil, false, false, nil
	}

	if entry.Flags == SSTableIndexKeyDelete {
		return nil, true, true, nil
	}

	pos := blockstore.Position{}
	pos.DecodeUint64(entry.DataFileOffSet)

	value, err = r.dataReader.ReadValueAt(pos)
	if err != nil {
		return nil, false, true, err
	}

	return value, false, true, nil
}
