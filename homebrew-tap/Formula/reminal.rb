class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.5.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.1/reminal_0.5.1_darwin_arm64.tar.gz"
      sha256 "3dd20d0a09b0cff9b7a9d68d264a1736de8b19ea7b6040ee9d5c358fe6246b9d"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.1/reminal_0.5.1_darwin_amd64.tar.gz"
      sha256 "3737d63d4e44c75e309bb7d32c7dd27e187e32207e59a2e1100135c7da67a990"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.1/reminal_0.5.1_linux_arm64.tar.gz"
      sha256 "67ac32d2dacd969977482601d86b0caa2a34b31c460d1a7aff63dae5a0aa65f4"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
