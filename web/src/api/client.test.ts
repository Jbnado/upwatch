import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./client";

// O servidor recusa com 415 corpo sem Content-Type JSON. O cabeçalho é
// posto num lugar só, em request, e estes testes garantem que nenhuma
// chamada com corpo escapa dele.
describe("request", () => {
  afterEach(() => vi.unstubAllGlobals());

  function stubFetch() {
    const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  function headersOf(fetchMock: ReturnType<typeof stubFetch>) {
    const init = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    return new Headers(init[1].headers);
  }

  it("manda Content-Type JSON quando há corpo", async () => {
    const fetchMock = stubFetch();

    await api.login("admin", "uma-senha-bem-longa");

    expect(headersOf(fetchMock).get("Content-Type")).toBe("application/json");
  });

  it("não declara tipo quando não há corpo", async () => {
    const fetchMock = stubFetch();

    await api.logout();

    expect(headersOf(fetchMock).get("Content-Type")).toBeNull();
  });
});
