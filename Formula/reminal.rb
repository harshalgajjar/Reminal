class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.11"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.11/reminal_0.7.11_darwin_arm64.tar.gz"
      sha256 "0a69fe905f0417d2fa50e2aa337dadaf1c318dc6fde2680871b40b9405af7528"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.11/reminal_0.7.11_darwin_amd64.tar.gz"
      sha256 "f93cf8c11a7efa1c46ca4f427364f04b3819413543fb503775807e76aba9580c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.11/reminal_0.7.11_linux_arm64.tar.gz"
      sha256 "ca3ff73c53be17ffb381464f17585eeff037ac9d773f0c55bf5667262ed877cb"
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
