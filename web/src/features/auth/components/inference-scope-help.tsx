export function InferenceScopeHelp() {
  return (
    <small>
      Anthropic Message Batches need chat:generate and messages:batches. Anthropic web search and fetch need chat:generate plus messages:web_search or messages:web_fetch. OpenAI Responses web search needs responses:generate and responses:web_search.
    </small>
  );
}
