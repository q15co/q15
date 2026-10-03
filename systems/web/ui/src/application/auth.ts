export interface OwnerAuthentication {
  signIn(): Promise<void>;
  createCredential(options: string): Promise<string>;
}
