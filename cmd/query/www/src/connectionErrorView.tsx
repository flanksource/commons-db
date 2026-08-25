export function ErrorView({ message }: { message: string }) {
  return <div className="whitespace-pre-wrap break-words text-destructive">{message}</div>;
}
