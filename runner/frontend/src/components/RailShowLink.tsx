// "Show in conversation", and the note it leaves for a row the chat has not drawn: one not loaded
// yet, or one held but older than the rows the chat draws (it draws the newest and more on scroll).
// Shared by the side panel's Live and History tabs.
export default function RailShowLink({ onShow, missing }: { onShow: () => void; missing: boolean }) {
  return (
    <>
      <button type="button" className="rail-link" data-testid="rail-show" onClick={onShow}>Show in conversation</button>
      {missing && <span className="rail-missing" role="status">Not in the part of the conversation the chat has drawn. Scroll up in the chat to reach it.</span>}
    </>
  )
}

/** The note alone, for a card whose own title brings it into view. */
export function ShowMissing() {
  return <span className="rail-missing" role="status">Not in the part of the conversation the chat has drawn. Scroll up in the chat to reach it.</span>
}
