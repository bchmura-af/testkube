package artifacts

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sync/atomic"

	"github.com/dustin/go-humanize"

	"github.com/kubeshop/testkube/pkg/ui"
)

type handler struct {
	uploader      Uploader
	processor     Processor
	postProcessor PostProcessor
	pathPrefix    string

	success   atomic.Uint32
	errors    atomic.Uint32
	totalSize atomic.Uint64
}

type Handler interface {
	Start() error
	Add(path string, file fs.File, stat fs.FileInfo) error
	End() error
}

type HandlerOpts func(h *handler)

func WithPostProcessor(postProcessor PostProcessor) HandlerOpts {
	return func(h *handler) {
		h.postProcessor = postProcessor
	}
}

func WithPathPrefix(pathPrefix string) HandlerOpts {
	return func(h *handler) {
		h.pathPrefix = pathPrefix
	}
}

func NewHandler(uploader Uploader, processor Processor, opts ...HandlerOpts) Handler {
	h := &handler{
		uploader:  uploader,
		processor: processor,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *handler) Start() (err error) {
	err = h.processor.Start()
	if err != nil {
		return err
	}
	if h.postProcessor != nil {
		err = h.postProcessor.Start()
		if err != nil {
			return err
		}
	}
	return h.uploader.Start()
}

func (h *handler) Add(path string, file fs.File, stat fs.FileInfo) (err error) {
	// Apply path prefix correctly
	uploadPath := path
	if h.pathPrefix != "" {
		uploadPath = filepath.Join(h.pathPrefix, uploadPath)
	}

	size := uint64(stat.Size())
	h.totalSize.Add(size)

	fmt.Printf(ui.LightGray("%s (%s)\n"), uploadPath, humanize.Bytes(uint64(stat.Size())))

	err = h.processor.Add(h.uploader, uploadPath, file, stat)
	if err == nil {
		h.success.Add(1)
	} else {
		h.errors.Add(1)
		fmt.Printf(ui.Red("%s: failed: %s"), uploadPath, err.Error())
	}
	if h.postProcessor != nil {
		err = h.postProcessor.Add(path)
		if err != nil {
			h.errors.Add(1)
			fmt.Printf(ui.Red("post processor error: %s: failed: %s"), path, err.Error())
		}
	}
	return err
}

func (h *handler) End() (err error) {
	fmt.Printf("\n")

	err = h.processor.End()
	if err != nil {
		go h.uploader.End()
		return err
	}
	err = h.uploader.End()
	if err != nil {
		return err
	}
	if h.postProcessor != nil {
		err = h.postProcessor.End()
		if err != nil {
			return err
		}
	}

	errs := h.errors.Load()
	success := h.success.Load()
	totalSize := h.totalSize.Load()
	if errs == 0 && success == 0 {
		fmt.Printf("No artifacts found.\n")
	} else {
		fmt.Printf("Found and uploaded %s files (%s).\n", ui.LightCyan(success), ui.LightCyan(humanize.Bytes(totalSize)))
	}
	if errs > 0 {
		return fmt.Errorf("  %d problems while uploading files", errs)
	}
	return nil
}
