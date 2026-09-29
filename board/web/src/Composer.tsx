import { useRef } from "react";

// Composer: the chat input everywhere — multi-line, grows with content,
// Enter sends, Shift+Enter breaks the line. Send stays disabled until a
// session is actually connected.
export function Composer({ value, onChange, onSend, disabled, placeholder }: {
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  disabled: boolean;
  placeholder: string;
}) {
  const ref = useRef<HTMLTextAreaElement>(null);

  const grow = () => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 160) + "px";
  };

  const submit = () => {
    if (disabled || !value.trim()) return;
    onSend();
    requestAnimationFrame(grow);
  };

  return (
    <form
      className="row composer"
      onSubmit={(e) => { e.preventDefault(); submit(); }}
    >
      <textarea
        ref={ref} rows={1} value={value} placeholder={placeholder}
        disabled={disabled}
        onChange={(e) => { onChange(e.target.value); grow(); }}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(); }
        }}
      />
      <button className="btn small" type="submit" disabled={disabled || !value.trim()}>
        Send
      </button>
    </form>
  );
}
