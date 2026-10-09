// @blerglab/chat — the public surface. Everything an app may import is re-exported here and
// nothing else is public.

// ─── Types: every event payload the transcript understands, and the session messages ─────────
export type {
  AgentEvent,
  AgentEventKind,
  AgentEventsReplayDone,
  AgentUserMessage,
  ArtifactPayload,
  AssistantTextPayload,
  CapabilitiesPayload,
  CapabilityGroup,
  CapabilityGroupId,
  CapabilityItem,
  CheckInPayload,
  CompactionPayload,
  EndedBy,
  ErrorPayload,
  InterruptSession,
  MessagingPayload,
  ModelChangedPayload,
  SessionEnded,
  SessionInfo,
  SessionMetaChanged,
  SessionStateChanged,
  SessionStatus,
  SessionTitleChanged,
  SetSessionModel,
  StartStage,
  StartStagePayload,
  StartStageState,
  StartedBy,
  StatusPayload,
  SubagentPayload,
  SubscribeAgentEvents,
  ToolCallPayload,
  ToolResultPayload,
  TurnDonePayload,
  UnsubscribeSession,
  UserMessagePayload,
} from './types'

// ─── Model: events → groups → what to draw; no React in it ──────────────────────────────────
export {
  HIDDEN_KINDS,
  OUTPUT_LINE_CAP,
  buildTimeline,
  describeInput,
  failureLine,
  formatDuration,
  groupStats,
  isVisibleEvent,
  outputText,
  previewLine,
  safeJSON,
  summaryText,
} from './model/toolGroups'
export type { CallDescription, GroupStats, TimelineItem, ToolEntry } from './model/toolGroups'
export { LONG_RUNNING_MS, STILL_RUNNING_MS, liveCalls, longRunningTitle, oldestLive, runningLabel, runningMs } from './model/liveCalls'
export type { LiveCall } from './model/liveCalls'
export { isHarnessText } from './model/harnessText'
export { MAX_ATTACHMENTS_PER_MESSAGE, MAX_ATTACHMENT_BYTES, attachmentNote, composeMessage, splitAttachmentNote } from './model/attachments'
export type { UploadedFile } from './model/attachments'
export {
  HTML_ARTIFACT_CSP,
  HTML_ARTIFACT_CSP_META,
  downloadFileName,
  fileCount,
  filenameFromDisposition,
  findLatest,
  formatSize,
  groupArtifacts,
  parseCsv,
  prettyJson,
  withCsp,
} from './model/artifacts'
export type { ArtifactGroup, ArtifactInfo, CsvResult } from './model/artifacts'
export { ARTIFACT_VIEWS, CSV_MAX_COLS, CSV_MAX_ROWS, TEXT_PREVIEW_CAP, TEXT_VIEW_MAX, blobTypeFor, isViewable, tooBigToPreview } from './model/artifactViews'
export type { ArtifactViewSpec } from './model/artifactViews'
export { INLINE_IMAGE_MAX, clearPreviewCache, loadPreviewUrl, previewKind } from './model/artifactPreview'
export type { PreviewKind } from './model/artifactPreview'
export { ClockOffsetContext } from './model/clockOffset'
export { TICK_MS, absoluteLabel, dayKey, dayLabel, formatElapsed, parseTs, relativeLabel, turnDuration } from './model/timeLabel'
export type { TimeOpts } from './model/timeLabel'
export { safeUrl } from './model/safeUrl'

// ─── Feedback: review of a file (anchored requests, an edited copy) and draw-over of an image ─
export { emptyReview, mergeReviews, newRequestId, parseReviewFile, reviewFileName } from './model/review'
export type { Anchor, ReviewFile, ReviewRequest } from './model/review'
export { anchorSelection, applyHighlights, describePlace, findQuote } from './model/anchor'
export type { AnchorOptions } from './model/anchor'
export { unifiedDiff, wordDiff } from './model/diff'
export type { DiffPart } from './model/diff'
export { composeMarkupMessage, composeReviewMessage } from './model/feedback'
export type { Pin } from './model/feedback'

// ─── Cards ──────────────────────────────────────────────────────────────────────────────────
export { createCardRegistry, defineCard } from './cards/registry'
export type { CardDefinition, CardProps, CardRegistry } from './cards/registry'
export { parseCardFence } from './cards/fence'
export type { CardFence } from './cards/fence'

// ─── Theme ──────────────────────────────────────────────────────────────────────────────────
export { TOKEN_NAMES, themeStyle } from './theme'
export type { ThemeTokenName, ThemeTokens } from './theme'

export { latestStartAttempt, placeholderAttempt, startPanelVisible, withSessionOutcome, STILL_STARTING_MS } from './model/startStages'
export type { AttemptStage, StartAttempt, StartSession } from './model/startStages'
export { END_REASON, describeSessionEnd, isTerminalStatus } from './model/sessionEnd'
export type { SessionEndLine, SessionEndSource } from './model/sessionEnd'
export { localStorageChatStorage } from './model/storage'
export type { ChatStorage } from './model/storage'

// ─── Components ─────────────────────────────────────────────────────────────────────────────
export { default as Markdown } from './components/Markdown'
export type { MarkdownProps } from './components/Markdown'
export { default as ChatView, ReadyCard } from './components/ChatView'
export type { ChatSlots, ChatViewProps } from './components/ChatView'
export { default as Transcript, HistorySkeleton, pairFooters, turnStartTimes, dayBreaksOf } from './components/Transcript'
export type { TranscriptProps } from './components/Transcript'
export { default as Composer } from './components/Composer'
export type { ComposerProps } from './components/Composer'
export { renderEvent, turnDoneText, ToolCard, EventRow } from './components/cards/EventCards'
export type { DescribeUserMessage, MessageFooter, RenderOptions, UserMessageLook } from './components/cards/EventCards'
export { ToolGroup } from './components/ToolGroup'
export { default as ArtifactCard, MediaPreview } from './components/ArtifactCard'
export { default as ArtifactViewer } from './components/ArtifactViewer'
export { default as FilesPanel } from './components/FilesPanel'
export { default as ReviewMode } from './components/review/ReviewMode'
export type { ReviewModeProps } from './components/review/ReviewMode'
export { default as PdfPages } from './components/review/PdfPages'
export type { PdfPagesProps } from './components/review/PdfPages'
export { default as MarkupMode } from './components/markup/MarkupMode'
export type { MarkupModeProps } from './components/markup/MarkupMode'
export { ComposerChips, MessageText } from './components/AttachmentChips'
export { default as StartProgress } from './components/StartProgress'
export { default as WorkingIndicator } from './components/WorkingIndicator'
export { TimeLabel, DayDivider } from './components/TimeLabel'
export { ChatContext, useChat } from './components/context'
export type { ChatContextValue } from './components/context'

// ─── Hooks ──────────────────────────────────────────────────────────────────────────────────
export { useSession, unmatchedQueued, OLDER_PAGE } from './hooks/useSession'
export type { SessionHandle, UseSessionOptions, QueuedMessage, PendingTurn } from './hooks/useSession'
export { useTranscriptStore } from './hooks/useTranscriptStore'
export type { SessionTranscript } from './hooks/useTranscriptStore'
export { useAttachments, resetKeptAttachments } from './hooks/useAttachments'
export type { AttachmentItem } from './hooks/useAttachments'
export { useArtifacts } from './hooks/useArtifacts'
export type { Artifacts, ArtifactsStatus } from './hooks/useArtifacts'
export { useTicker, usePrefersReducedMotion } from './hooks/useTicker'
export { useClockNow } from './hooks/useSharedClock'
export { useSecondClock } from './hooks/useSecondClock'
export { useBackdropClose } from './hooks/useBackdropClose'

// ─── Transport ──────────────────────────────────────────────────────────────────────────────
export * from './transport'
