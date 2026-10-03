export async function logout() {
  const response = await fetch("/auth/logout", {
    method: "POST",
    credentials: "same-origin",
    cache: "no-store",
  });
  if (response.status !== 204 && response.status !== 401) throw new Error("Sign-out failed.");
  location.replace("/");
}
