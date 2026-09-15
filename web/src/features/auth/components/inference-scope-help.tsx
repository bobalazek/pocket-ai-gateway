export function InferenceScopeHelp() {
  return (
    <small>
      Anthropic Message Batches need chat:generate and messages:batches. Anthropic web search needs chat:generate and messages:web_search. OpenAI Responses web search needs responses:generate and responses:web_search.
    </small>
  );
}
