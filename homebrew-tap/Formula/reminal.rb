class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.5.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.0/reminal_0.5.0_darwin_arm64.tar.gz"
      sha256 "3d93f95865ef7c375aa833332d4827689942fe251bf0708a6fb3a16895fb6ba6"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.0/reminal_0.5.0_darwin_amd64.tar.gz"
      sha256 "444debc3f3dbad35ee294f4c139dc30cd78563061a985e23d1981eb891340ea4"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.0/reminal_0.5.0_linux_arm64.tar.gz"
      sha256 "392b22f3286bf85ccbeaa7966ba994d7ebffbcbcb0b7ac45a0d98cadcbc02275"
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
