/** The host supplies recipient-scoped metadata; inbox content never enters a shared transcript. */
export default function InboxArrivalCard({ subject, onOpen }: { subject: string; onOpen(): void }) {
  return <div className="agent-card delegation-card" data-testid="inbox-arrival"><div className="delegation-head"><span className="delegation-title">{subject}</span></div><div className="delegation-actions"><button type="button" className="artifact-btn" onClick={onOpen}>Open inbox</button></div></div>
}
