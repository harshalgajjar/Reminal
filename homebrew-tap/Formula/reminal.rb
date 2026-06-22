class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.5.5"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.5/reminal_0.5.5_darwin_arm64.tar.gz"
      sha256 "33f66e7dcdb1e1a29d75259f48eae782a321d6a4a523ec6264c0c6ed7c2f6bdc"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.5/reminal_0.5.5_darwin_amd64.tar.gz"
      sha256 "7f85cca77a2ffd778ab307b6ebad231ee84850cbfd8a43b67c9e52510ef99ef6"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.5.5/reminal_0.5.5_linux_arm64.tar.gz"
      sha256 "cee7eeeff5c9db96ded2ab8946faf107471a077330b05ec75a54c07555e2996e"
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
