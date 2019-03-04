package blockstore

import (
	"bytes"
	"io"
)

type inMemBlock struct {
	data []byte
}

func newInMemBlock(size int) inMemBlock {
	return inMemBlock{
		data: make([]byte, size, size),
	}
}

// Im Memory block storage is uesd for testing only
type InMemBlockStorage struct {
	blocks   []inMemBlock
	blockLen int

	blockSize int

	seqReadBlock  int
	seqReadOffset int

	seqWriteBlock  int
	seqWriteOffset int
}

func NewInMemBlockStorage(blockSize int) BlockStorage {
	return &InMemBlockStorage{
		blockSize:      blockSize,
		blockLen:       -1,
		blocks:         make([]inMemBlock, 0, 1),
		seqWriteBlock:  blockSize,
		seqWriteOffset: blockSize,
		seqReadBlock:   0,
		seqReadOffset:  0,
	}
}

func (b *InMemBlockStorage) blockStorage() {}

func (b *InMemBlockStorage) growBlocks() {
	if b.blockLen > len(b.blocks) {
		for i := len(b.blocks); i < b.blockLen; i++ {
			b.blocks = append(b.blocks, newInMemBlock(b.blockSize))
		}
	}
}

// Pack bytes into one block at a time, start a new block
// if space left in current block is too small
func (b *InMemBlockStorage) Write(p []byte) (n int, err error) {

	if len(p) < 1 {
		return 0, nil
	}
	if len(p) > b.blockSize {
		return 0, SizeExceeded
	}

	remaining := b.blockSize - b.seqWriteOffset
	writeSize := len(p)

	if remaining < writeSize {
		b.seqWriteOffset = 0
		b.seqWriteBlock++
	}

	if b.blockLen < b.seqWriteBlock {
		b.blockLen = b.seqWriteBlock
		b.growBlocks()
	}

	//copy(b, p)
	nCopied := copy(b.blocks[b.seqWriteBlock].data[b.seqWriteOffset:], p)
	b.seqWriteOffset += nCopied

	return nCopied, nil
}

// Read bytes from block
// starts a new block if remaining size is too small
func (b *InMemBlockStorage) Read(p []byte) (n int, err error) {

	remaining := b.blockSize - b.seqReadOffset

	if len(p) < 1 {
		return 0, nil
	}

	readSize := len(p)

	if readSize < remaining {
		b.seqReadOffset = 0
		b.seqReadBlock++
	}

	if b.blockLen < b.seqReadBlock {
		return 0, io.EOF
	}

	nCopied := copy(p, b.blocks[b.seqReadBlock].data[b.seqReadOffset:])

	b.seqReadOffset += nCopied

	return nCopied, nil
}

func (b *InMemBlockStorage) Flush() error {
	return nil
}

func (b *InMemBlockStorage) Close() error {
	return nil
}

func (b *InMemBlockStorage) Sync() error {
	return nil
}

// Read block into a buffer
//
func (b *InMemBlockStorage) ReadBlock(index uint, buffer *bytes.Buffer) (n int, err error) {
	if b.blockLen > int(index) {
		return 0, io.EOF
	}

	block := &b.blocks[int(index)]
	nCopied, err := buffer.Write(block.data)

	return nCopied, err
}

// write data in buffer into a block.
func (b *InMemBlockStorage) WriteBlock(index uint, buffer *bytes.Buffer) (n int, err error) {
	if b.blockLen > int(index) {
		return 0, io.EOF
	}

	block := &b.blocks[int(index)]
	minSize := buffer.Len()
	if minSize > b.blockSize {
		minSize = b.blockSize
	}

	nCopied := copy(block.data, buffer.Bytes()[0:minSize])

	return nCopied, nil
}

func (b *InMemBlockStorage) BlockSize() int {
	return b.blockSize
}

func (b *InMemBlockStorage) NumBlocks() int {
	return b.blockLen
}
