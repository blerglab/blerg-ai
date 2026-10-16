// The server's id for a failure, drawn small after its sentence. The runner writes the same id in
// its log line for the failure, so whoever runs Blerg can find what happened.
export default function ErrorId({ id }: { id?: string }) {
  if (!id) return null
  return (
    <span
      className="chat-error-id"
      data-testid="error-id"
      title="Give this id to whoever runs Blerg: the runner's log has the same one."
    >
      {' '}(error id {id})
    </span>
  )
}
