class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.3"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.3/reminal_0.7.3_darwin_arm64.tar.gz"
      sha256 "2e490669d77f337af00cb695e70fa7f7ba359d8ecf5c8efbd8978873f98b13bf"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.3/reminal_0.7.3_darwin_amd64.tar.gz"
      sha256 "610927f62ff9a70e22dfa89e852a08c152fbce7864cd4f088e0c15dd62db64fb"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.3/reminal_0.7.3_linux_arm64.tar.gz"
      sha256 "1e978fd6ae3f7bc697efff557f7da9fc1f05b3a75842b2a33d6aed7d08554d5e"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
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
