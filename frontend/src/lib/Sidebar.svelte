<script lang="ts">
  import { ENABLED_NAV_KEYS, NAV, type NavEntry } from './nav'

  let {
    active,
    summary,
    onselect,
  }: {
    active: string
    summary: string
    onselect: (entry: NavEntry) => void
  } = $props()

  const entries = NAV
</script>

<nav>
  <h1>工作区</h1>
  <ul>
    {#each entries as entry (entry.key)}
      {@const enabled = ENABLED_NAV_KEYS.has(entry.key)}
      <li>
        <button
          class="nav"
          class:selected={active === entry.key}
          disabled={!enabled}
          title={enabled ? entry.label : `${entry.label}（尚未移植）`}
          onclick={() => onselect(entry)}
        >
          {entry.label}
        </button>
      </li>
    {/each}
  </ul>
  <hr />
  <p class="summary">{summary}</p>
</nav>

<style>
  nav {
    width: var(--sidebar-w);
    flex: 0 0 var(--sidebar-w);
    background: var(--nav);
    color: #cfd8dc;
    display: flex;
    flex-direction: column;
    padding: 10px 8px;
    overflow-y: auto;
  }
  h1 {
    font-size: 13px;
    margin: 0 0 10px;
    padding-left: 4px;
    color: #eceff1;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  button.nav {
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    border-radius: 4px;
    color: #cfd8dc;
    padding: 6px 8px;
  }
  button.nav:hover:not(:disabled) {
    background: var(--nav-hover);
  }
  button.nav:active:not(:disabled) {
    background: var(--nav-pressed);
  }
  button.nav.selected {
    background: #fff;
    color: var(--sel-fg);
  }
  button.nav:disabled {
    opacity: 0.38;
    cursor: default;
  }
  hr {
    width: 100%;
    border: 0;
    border-top: 1px solid var(--nav-hover);
    margin: 10px 0;
  }
  .summary {
    margin: 0;
    padding: 0 4px;
    font-size: 12px;
    line-height: 1.5;
    color: #90a4ae;
    /* §S2: the summary label wraps. */
    overflow-wrap: anywhere;
  }
</style>
