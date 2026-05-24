package handlers

// OFFICE-26: Comments — anchored, threaded, resolvable.
// REST endpoints:
//   GET    /api/files/:id/comments           → list comments + replies for a file
//   POST   /api/files/:id/comments           → add a comment
//   PUT    /api/files/:id/comments/:cid      → edit body or change state (resolve/reopen)
//   DELETE /api/files/:id/comments/:cid      → delete a comment
//   POST   /api/files/:id/comments/:cid/replies   → add a reply
//   PUT    /api/files/:id/comments/:cid/replies/:rid → edit a reply
//   DELETE /api/files/:id/comments/:cid/replies/:rid → tombstone a reply

import (
	"net/http"
	"time"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type CommentHandler struct {
	store storage.Storage
}

func NewCommentHandler(store storage.Storage) *CommentHandler {
	return &CommentHandler{store: store}
}

// hlcNow returns a simple HLC-compatible clock string (wall-ms padded, monotone via uuid suffix).
func hlcNow() string {
	return time.Now().UTC().Format("20060102150405.000") + "-" + uuid.New().String()[:8]
}

// CommentWithReplies is the wire shape returned by List.
type CommentWithReplies struct {
	*models.Comment
	Replies []*models.CommentReply `json:"replies"`
}

// List returns all comments for a file with their replies.
func (h *CommentHandler) List(c *gin.Context) {
	fileID := c.Param("id")
	comments, err := h.store.ListComments(fileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if comments == nil {
		comments = []*models.Comment{}
	}

	result := make([]CommentWithReplies, 0, len(comments))
	for _, cm := range comments {
		replies, _ := h.store.ListReplies(cm.ID)
		if replies == nil {
			replies = []*models.CommentReply{}
		}
		result = append(result, CommentWithReplies{Comment: cm, Replies: replies})
	}
	c.JSON(http.StatusOK, result)
}

// Create adds a new comment anchored to a file location.
func (h *CommentHandler) Create(c *gin.Context) {
	var req models.CreateCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	cm := &models.Comment{
		ID:       uuid.New().String(),
		FileID:   c.Param("id"),
		Anchor:   req.Anchor,
		AuthorID: req.AuthorID,
		Body:     req.Body,
		State:    models.CommentOpen,
		SeqClock: hlcNow(),
	}
	if err := h.store.CreateComment(cm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, cm)
}

// Update edits the body or state of a comment.
func (h *CommentHandler) Update(c *gin.Context) {
	fileID := c.Param("id")
	commentID := c.Param("cid")

	cm, err := h.store.GetComment(fileID, commentID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "comment not found"})
		return
	}

	var req models.UpdateCommentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Body != "" {
		cm.Body = req.Body
	}
	if req.State != "" {
		cm.State = req.State
	}
	cm.SeqClock = hlcNow()

	if err := h.store.UpdateComment(cm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, cm)
}

// Delete removes a comment.
func (h *CommentHandler) Delete(c *gin.Context) {
	fileID := c.Param("id")
	commentID := c.Param("cid")
	if err := h.store.DeleteComment(fileID, commentID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "comment not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// CreateReply adds a threaded reply to a comment.
func (h *CommentHandler) CreateReply(c *gin.Context) {
	fileID := c.Param("id")
	commentID := c.Param("cid")

	// Ensure the comment exists.
	if _, err := h.store.GetComment(fileID, commentID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "comment not found"})
		return
	}

	var req models.CreateReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	r := &models.CommentReply{
		ID:        uuid.New().String(),
		CommentID: commentID,
		FileID:    fileID,
		AuthorID:  req.AuthorID,
		Body:      req.Body,
		SeqClock:  hlcNow(),
		Deleted:   false,
	}
	if err := h.store.CreateReply(r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, r)
}

// UpdateReply edits the body of a reply.
func (h *CommentHandler) UpdateReply(c *gin.Context) {
	commentID := c.Param("cid")
	replyID := c.Param("rid")

	r, err := h.store.GetReply(commentID, replyID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "reply not found"})
		return
	}

	var req models.UpdateReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Body != "" {
		r.Body = req.Body
	}
	r.SeqClock = hlcNow()

	if err := h.store.UpdateReply(r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, r)
}

// DeleteReply tombstones a reply (soft-delete for CRDT convergence).
func (h *CommentHandler) DeleteReply(c *gin.Context) {
	commentID := c.Param("cid")
	replyID := c.Param("rid")

	r, err := h.store.GetReply(commentID, replyID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "reply not found"})
		return
	}

	r.Deleted = true
	r.Body = ""
	r.SeqClock = hlcNow()

	if err := h.store.UpdateReply(r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, r)
}
