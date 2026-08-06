package modelmeta

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

// The safetensors container is specified as: 8 bytes of unsigned little-endian
// header length N, N bytes of UTF-8 JSON header mapping tensor name to
// {dtype, shape, data_offsets}, then the data. Summing prod(shape) over the
// header therefore yields the exact element count without touching the weights.
const (
	safetensorsExt          = ".safetensors"
	safetensorsHeaderLenLen = 8
	// maxSafetensorsHeaderBytes rejects a corrupt or hostile length prefix before
	// it turns into an allocation. Real headers are tens of kilobytes; a hundred
	// megabytes is orders of magnitude past any of them.
	maxSafetensorsHeaderBytes = 100 << 20
	// metadataKey is the header's one reserved, non-tensor key.
	metadataKey = "__metadata__"
)

// safetensorsTensor is the per-tensor header entry; only the shape matters here.
type safetensorsTensor struct {
	Shape []int64 `json:"shape"`
}

// sumSafetensorsElements returns the total element count across every
// *.safetensors shard directly under dir, or nil when there are none.
//
// Shards are enumerated by globbing rather than by reading
// model.safetensors.index.json: that index carries only a total byte size and a
// tensor-to-shard map, no shapes, so it cannot produce a count. Deriving a count
// from the byte size would mean assuming a dtype, which is an estimate.
func sumSafetensorsElements(dir string) (*int64, error) {
	shards, err := filepath.Glob(filepath.Join(dir, "*"+safetensorsExt))
	if err != nil {
		return nil, err
	}

	if len(shards) == 0 {
		return nil, nil
	}

	var total int64

	for _, shard := range shards {
		count, err := countShardElements(shard)
		if err != nil {
			return nil, errors.Wrapf(err, "read safetensors header of %s", filepath.Base(shard))
		}

		if count > math.MaxInt64-total {
			return nil, errors.Errorf("element count of %s overflows", filepath.Base(shard))
		}

		total += count
	}

	return &total, nil
}

func countShardElements(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}

	var headerLen uint64
	if err := binary.Read(file, binary.LittleEndian, &headerLen); err != nil {
		return 0, errors.Wrap(err, "read header length")
	}

	if headerLen == 0 || headerLen > maxSafetensorsHeaderBytes {
		return 0, errors.Errorf("implausible header length %d", headerLen)
	}

	if int64(headerLen) > stat.Size()-safetensorsHeaderLenLen {
		return 0, errors.Errorf("header length %d exceeds file size %d", headerLen, stat.Size())
	}

	raw := make([]byte, headerLen)
	if _, err := io.ReadFull(file, raw); err != nil {
		return 0, errors.Wrap(err, "read header")
	}

	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return 0, errors.Wrap(err, "parse header")
	}

	var total int64

	for name, entry := range header {
		if name == metadataKey {
			continue
		}

		var tensor safetensorsTensor
		if err := json.Unmarshal(entry, &tensor); err != nil {
			return 0, errors.Wrapf(err, "parse tensor %q", name)
		}

		elements, err := elementCount(tensor.Shape)
		if err != nil {
			return 0, errors.Wrapf(err, "tensor %q", name)
		}

		if elements > math.MaxInt64-total {
			return 0, errors.New("element count overflows")
		}

		total += elements
	}

	return total, nil
}

// elementCount is prod(shape). An empty shape is a scalar, so the product starts
// at one.
func elementCount(shape []int64) (int64, error) {
	count := int64(1)

	for _, dim := range shape {
		if dim < 0 {
			return 0, errors.Errorf("negative dimension %d", dim)
		}

		if dim != 0 && count > math.MaxInt64/dim {
			return 0, errors.New("shape product overflows")
		}

		count *= dim
	}

	return count, nil
}
