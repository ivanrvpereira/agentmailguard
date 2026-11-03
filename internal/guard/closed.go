package guard

import (
	"context"
	"slices"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

const (
	FolderTrash  = GuardError("refused: folder is trash")
	FolderSpam   = GuardError("refused: folder is spam")
	MessageTrash = GuardError("refused: message is in trash")
	MessageSpam  = GuardError("refused: message is in spam")
	MoveTrash    = GuardError("move refused: use mail.trash")
	MoveSpam     = GuardError("move refused: use mail.spam")
)

func (g *Guard) closedFolders(ctx context.Context) (map[string]model.Bin, error) {
	closed, err := g.mailbox.ClosedFolders(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	if len(closed) == 0 {
		return nil, GuardError("ValueError")
	}
	for id, bin := range closed {
		if id == "" || bin != model.Trash && bin != model.Spam {
			return nil, GuardError("ValueError")
		}
	}
	return closed, nil
}

func (g *Guard) folderBin(ctx context.Context, folder string) (model.Bin, error) {
	closed, err := g.closedFolders(ctx)
	if err != nil {
		return "", err
	}
	id, err := g.mailbox.ResolveFolder(ctx, folder)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", safeErr(ctx, err)
	}
	if id == "" {
		return "", GuardError("ValueError")
	}
	return closed[id], nil
}

func (g *Guard) openFolder(ctx context.Context, folder string) error {
	bin, err := g.folderBin(ctx, folder)
	if err != nil {
		return err
	}
	switch bin {
	case model.Trash:
		return FolderTrash
	case model.Spam:
		return FolderSpam
	}
	return nil
}

func messageClosed(m model.Message, closed map[string]model.Bin) error {
	for _, folder := range m.Folders {
		switch closed[folder] {
		case model.Trash:
			return MessageTrash
		case model.Spam:
			return MessageSpam
		}
	}
	return nil
}

func (g *Guard) openMessage(ctx context.Context, id string) error {
	closed, err := g.closedFolders(ctx)
	if err != nil {
		return err
	}
	messages, err := g.fetch(ctx, []string{id}, false)
	if err != nil {
		return err
	}
	return messageClosed(messages[id], closed)
}

func (g *Guard) openMessages(ctx context.Context, messages []model.Message) ([]model.Message, error) {
	closed, err := g.closedFolders(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(messages), func(m model.Message) bool { return messageClosed(m, closed) != nil }), nil
}
